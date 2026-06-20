package activity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// activityColumns is the canonical select list, kept in one place so the
// scan order can't drift between queries.
const activityColumns = `
	id, user_id, activity_type, ingest_source, source_activity_id,
	start_time, name, distance_meters, duration_seconds,
	avg_pace_sec_per_km, best_pace_sec_per_km,
	avg_heart_rate_bpm, max_heart_rate_bpm, total_calories, elevation_gain_meters,
	tcx_s3_key, created_at, deleted_at`

type SQLiteRepository struct {
	db       *sql.DB
	archiver Archiver
	now      func() time.Time
}

func NewSQLiteRepository(db *sql.DB, archiver Archiver) *SQLiteRepository {
	return &SQLiteRepository{db: db, archiver: archiver, now: time.Now}
}

// Create inserts the activity + its trackpoints in one transaction and
// archives the TCX file. The ordering matters for consistency:
//
//	BEGIN → INSERT activity (+ trackpoints) → archive Put → COMMIT
//
// The row + its points go in first so a UNIQUE violation short-circuits
// before we touch S3. The Put happens before COMMIT so a storage failure
// rolls the whole thing back — we never persist an activity whose file
// isn't in the bucket. If COMMIT itself fails after a successful Put, we
// best-effort Delete the orphaned object.
//
// Pre-conditions on a: ActivityType, IngestSource, SourceActivityID,
// StartTime, and the running-specific fields if applicable are already
// populated by the caller (typically IngestTCX). Create generates ID and
// stamps TCXS3Key and CreatedAt.
func (r *SQLiteRepository) Create(ctx context.Context, a *Activity, tcx []byte) error {
	now := r.now().UTC()
	a.ID = id.New()
	key, err := buildTCXKey(a.UserID, a.ActivityType, a.StartTime, a.ID)
	if err != nil {
		return err
	}
	a.TCXS3Key = key
	a.CreatedAt = now
	a.DeletedAt = nil

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback is a no-op once Commit succeeds; safe to always defer.
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, name, distance_meters, duration_seconds,
			avg_pace_sec_per_km, best_pace_sec_per_km,
			avg_heart_rate_bpm, max_heart_rate_bpm, total_calories, elevation_gain_meters,
			tcx_s3_key, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, a.ID, a.UserID, a.ActivityType, a.IngestSource, a.SourceActivityID,
		a.StartTime, a.Name, a.DistanceMeters, a.DurationSeconds,
		a.AvgPaceSecPerKm, a.BestPaceSecPerKm,
		a.AvgHeartRateBpm, a.MaxHeartRateBpm, a.TotalCalories, a.ElevationGainMeters,
		a.TCXS3Key, a.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return err
	}

	if err := insertTrackpointsTx(ctx, tx, a.ID, a.Trackpoints); err != nil {
		return err
	}

	if err := insertBestEffortsTx(ctx, tx, a.ID, a.BestEfforts); err != nil {
		return err
	}

	// Archive before COMMIT so a storage failure aborts the whole write.
	if err := r.archiver.Put(ctx, a.TCXS3Key, tcx, ObjectMetadata{IngestSource: a.IngestSource}); err != nil {
		return fmt.Errorf("%w: %w", ErrStorage, err)
	}

	if err := tx.Commit(); err != nil {
		// The object is in S3 but the row didn't land — clean up so we
		// don't leak an orphan. Best-effort; ignore the delete error.
		_ = r.archiver.Delete(ctx, a.TCXS3Key)
		return err
	}
	return nil
}

func insertTrackpointsTx(ctx context.Context, tx *sql.Tx, activityID string, points []Trackpoint) error {
	if len(points) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO activity_trackpoints (
			activity_id, sequence, elapsed_seconds, distance_meters,
			heart_rate_bpm, pace_sec_per_km, elevation_meters
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range points {
		if _, err := stmt.ExecContext(ctx, activityID, p.Sequence, p.ElapsedSeconds,
			p.DistanceMeters, p.HeartRateBpm, p.PaceSecPerKm, p.ElevationMeters); err != nil {
			return err
		}
	}
	return nil
}

// insertBestEffortsTx writes one activity_best_efforts row per computed
// best effort, inside the caller's Create transaction so they roll back
// with everything else on a dedup violation or storage failure.
func insertBestEffortsTx(ctx context.Context, tx *sql.Tx, activityID string, efforts []ActivityBestEffort) error {
	if len(efforts) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO activity_best_efforts (activity_id, distance_key, duration_seconds)
		VALUES (?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range efforts {
		if _, err := stmt.ExecContext(ctx, activityID, e.DistanceKey, e.DurationSeconds); err != nil {
			return err
		}
	}
	return nil
}

// GetUserRunningBestEfforts returns the per-distance current best for the
// user. ROW_NUMBER partitions by distance_key ordered by duration asc then
// start_time asc, so rn=1 is the fastest window at each distance with the
// earliest activity winning duration ties.
func (r *SQLiteRepository) GetUserRunningBestEfforts(ctx context.Context, userID string) ([]RunningBestEffort, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT distance_key, duration_seconds, activity_id, start_time
		FROM (
			SELECT
				e.distance_key,
				e.duration_seconds,
				e.activity_id,
				a.start_time,
				ROW_NUMBER() OVER (
					PARTITION BY e.distance_key
					ORDER BY e.duration_seconds ASC, a.start_time ASC
				) AS rn
			FROM activity_best_efforts e
			JOIN activities a ON a.id = e.activity_id
			WHERE a.user_id = ? AND a.deleted_at IS NULL AND a.activity_type = ?
		)
		WHERE rn = 1
	`, userID, ActivityRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RunningBestEffort
	for rows.Next() {
		var b RunningBestEffort
		if err := rows.Scan(&b.DistanceKey, &b.DurationSeconds, &b.ActivityID, &b.ActivityStartTime); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetRunningBestEffortHistory returns every best-effort row at distanceKey
// for the user's live running activities, ascending by start_time.
func (r *SQLiteRepository) GetRunningBestEffortHistory(ctx context.Context, userID, distanceKey string) ([]BestEffortPoint, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.activity_id, a.start_time, e.duration_seconds, a.distance_meters
		FROM activity_best_efforts e
		JOIN activities a ON a.id = e.activity_id
		WHERE a.user_id = ? AND a.deleted_at IS NULL AND a.activity_type = ? AND e.distance_key = ?
		ORDER BY a.start_time ASC
	`, userID, ActivityRunning, distanceKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BestEffortPoint
	for rows.Next() {
		var p BestEffortPoint
		if err := rows.Scan(&p.ActivityID, &p.ActivityStartTime, &p.DurationSeconds, &p.ActivityDistanceMeters); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) GetBySourceActivityID(ctx context.Context, userID string, source IngestSource, sourceActivityID string) (*Activity, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+activityColumns+`
		FROM activities
		WHERE user_id = ? AND ingest_source = ? AND source_activity_id = ? AND deleted_at IS NULL
	`, userID, source, sourceActivityID)
	a, err := scanActivity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (r *SQLiteRepository) List(ctx context.Context, userID string, limit int, before *time.Time) ([]Activity, error) {
	args := []any{userID}
	// strength_training rows live in this table but their canonical surface
	// is the workout they enrich, not the standalone activities feed — so the
	// feed and the day-bucketed overview (which calls List/ListInRange)
	// exclude them. The type-scoped running queries are unaffected.
	clauses := []string{"user_id = ?", "deleted_at IS NULL", "activity_type != 'strength_training'"}
	if before != nil {
		clauses = append(clauses, "start_time < ?")
		args = append(args, *before)
	}
	q := `
		SELECT ` + activityColumns + `
		FROM activities
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY start_time DESC`
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Activity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) ListInRange(ctx context.Context, userID string, since, until *time.Time) ([]Activity, error) {
	args := []any{userID}
	// See List: strength_training rows are excluded from the activities feed
	// and the day-bucketed overview that this range query backs.
	clauses := []string{"user_id = ?", "deleted_at IS NULL", "activity_type != 'strength_training'"}
	if since != nil {
		clauses = append(clauses, "start_time >= ?")
		args = append(args, *since)
	}
	if until != nil {
		// Half-open interval — an activity at exactly `until` belongs to
		// the next window (callers chain adjacent month boundaries).
		clauses = append(clauses, "start_time < ?")
		args = append(args, *until)
	}
	q := `
		SELECT ` + activityColumns + `
		FROM activities
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY start_time DESC`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Activity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) Get(ctx context.Context, userID, activityID string) (*Activity, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+activityColumns+`
		FROM activities
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, activityID, userID)
	a, err := scanActivity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	points, err := r.loadTrackpoints(ctx, activityID)
	if err != nil {
		return nil, err
	}
	a.Trackpoints = points
	return a, nil
}

func (r *SQLiteRepository) SummariesByIDs(ctx context.Context, userID string, ids []string) (map[string]Activity, error) {
	out := make(map[string]Activity, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	// No activity_type filter: callers fetch by explicit id and want the
	// strength_training rows that List/ListInRange exclude from the feed.
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+activityColumns+`
		FROM activities
		WHERE user_id = ? AND deleted_at IS NULL AND id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out[a.ID] = *a
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) loadTrackpoints(ctx context.Context, activityID string) ([]Trackpoint, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT sequence, elapsed_seconds, distance_meters,
		       heart_rate_bpm, pace_sec_per_km, elevation_meters
		FROM activity_trackpoints
		WHERE activity_id = ?
		ORDER BY sequence ASC
	`, activityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Trackpoint
	for rows.Next() {
		var (
			p  Trackpoint
			hr sql.NullInt64
			pa sql.NullFloat64
			el sql.NullFloat64
		)
		if err := rows.Scan(&p.Sequence, &p.ElapsedSeconds, &p.DistanceMeters, &hr, &pa, &el); err != nil {
			return nil, err
		}
		if hr.Valid {
			v := int(hr.Int64)
			p.HeartRateBpm = &v
		}
		if pa.Valid {
			v := pa.Float64
			p.PaceSecPerKm = &v
		}
		if el.Valid {
			v := el.Float64
			p.ElevationMeters = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) Rename(ctx context.Context, userID, activityID, name string) (*Activity, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activities
		SET name = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, name, activityID, userID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	// Re-read so the returned activity reflects exactly what's persisted.
	row := r.db.QueryRowContext(ctx, `
		SELECT `+activityColumns+`
		FROM activities
		WHERE id = ? AND user_id = ?
	`, activityID, userID)
	return scanActivity(row)
}

func (r *SQLiteRepository) SoftDelete(ctx context.Context, userID, activityID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE activities
		SET deleted_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, activityID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) RunningMetrics(ctx context.Context, userID string, now time.Time, loc *time.Location) (Metrics, error) {
	// Pull the minimal projection for every live ActivityRunning row and
	// aggregate in Go. Week/month boundaries are user-local; SQLite's
	// date() modifier only takes a fixed UTC offset and is wrong across
	// DST, so the bucketing can't live in SQL. Personal-scale data makes
	// the full scan cheap and the code far clearer than window SQL.
	//
	// The (user_id, activity_type, start_time DESC) WHERE deleted_at IS NULL
	// partial index covers this query exactly.
	rows, err := r.db.QueryContext(ctx, `
		SELECT start_time, distance_meters, duration_seconds
		FROM activities
		WHERE user_id = ? AND activity_type = ? AND deleted_at IS NULL
	`, userID, ActivityRunning)
	if err != nil {
		return Metrics{}, err
	}
	defer rows.Close()

	var data []metricRow
	for rows.Next() {
		var mr metricRow
		if err := rows.Scan(&mr.startTime, &mr.distanceMeters, &mr.durationSeconds); err != nil {
			return Metrics{}, err
		}
		data = append(data, mr)
	}
	if err := rows.Err(); err != nil {
		return Metrics{}, err
	}
	return computeMetrics(data, now, loc), nil
}

// ListRunningSamplesSince returns the (start_time, distance_meters) projection
// for the user's live ActivityRunning rows starting at/after `since`. Mirrors
// RunningMetrics' filter (running type, deleted_at IS NULL); the handler
// buckets the samples into local weeks. The
// (user_id, activity_type, start_time DESC) WHERE deleted_at IS NULL partial
// index covers this query.
func (r *SQLiteRepository) ListRunningSamplesSince(ctx context.Context, userID string, since time.Time) ([]RunSample, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT start_time, distance_meters
		FROM activities
		WHERE user_id = ? AND activity_type = ? AND deleted_at IS NULL AND start_time >= ?
	`, userID, ActivityRunning, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RunSample
	for rows.Next() {
		var rs RunSample
		if err := rows.Scan(&rs.StartTime, &rs.DistanceMeters); err != nil {
			return nil, err
		}
		out = append(out, rs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanActivity reads one activities row out of a Row or Rows.
func scanActivity(s interface{ Scan(...any) error }) (*Activity, error) {
	var (
		act       Activity
		name      sql.NullString
		avgPace   sql.NullFloat64
		bestPace  sql.NullFloat64
		avgHR     sql.NullInt64
		maxHR     sql.NullInt64
		calories  sql.NullInt64
		elevation sql.NullFloat64
		deletedAt sql.NullTime
	)
	if err := s.Scan(
		&act.ID, &act.UserID, &act.ActivityType, &act.IngestSource, &act.SourceActivityID,
		&act.StartTime, &name, &act.DistanceMeters, &act.DurationSeconds,
		&avgPace, &bestPace,
		&avgHR, &maxHR, &calories, &elevation,
		&act.TCXS3Key, &act.CreatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if name.Valid {
		v := name.String
		act.Name = &v
	}
	if avgPace.Valid {
		v := avgPace.Float64
		act.AvgPaceSecPerKm = &v
	}
	if bestPace.Valid {
		v := bestPace.Float64
		act.BestPaceSecPerKm = &v
	}
	if avgHR.Valid {
		v := int(avgHR.Int64)
		act.AvgHeartRateBpm = &v
	}
	if maxHR.Valid {
		v := int(maxHR.Int64)
		act.MaxHeartRateBpm = &v
	}
	if calories.Valid {
		v := int(calories.Int64)
		act.TotalCalories = &v
	}
	if elevation.Valid {
		v := elevation.Float64
		act.ElevationGainMeters = &v
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		act.DeletedAt = &t
	}
	return &act, nil
}

// isUniqueViolation reports whether err is the go-sqlite3 UNIQUE
// constraint failure raised by the (user_id, ingest_source,
// source_activity_id) index. We check the extended code first; the
// string fallback covers wrapped or non-driver errors carrying the
// same message.
func isUniqueViolation(err error) bool {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return se.ExtendedCode == sqlite3.ErrConstraintUnique
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
