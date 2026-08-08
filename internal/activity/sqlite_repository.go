package activity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/Prog-Strength/prog-strength-api/internal/hrzones"
	"github.com/Prog-Strength/prog-strength-api/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// Since migration 042 the activities table is the base for EVERY session type;
// the endurance summary columns live in per-type detail tables. Reads LEFT
// JOIN all four detail tables and COALESCE per column — at most one detail
// row exists per activity, so the COALESCE picks the only non-NULL branch.
// Strength rows have no detail row; their endurance fields fall back to the
// zero values the Activity struct always used for them.
const activityJoins = `
	FROM activities a
	LEFT JOIN activity_run_details rd ON rd.activity_id = a.id
	LEFT JOIN activity_walk_details wd ON wd.activity_id = a.id
	LEFT JOIN activity_cycle_details cd ON cd.activity_id = a.id
	LEFT JOIN activity_other_details od ON od.activity_id = a.id
	LEFT JOIN activity_hike_details hd ON hd.activity_id = a.id`

// detailCoalesce projects one endurance column across the four detail tables
// joined by activityJoins: at most one detail row exists per activity, so the
// COALESCE picks the only non-NULL branch. fallback (a SQL literal; "" for
// none) covers base-only rows so NOT NULL struct fields scan cleanly. EVERY
// detail-column projection must go through this builder — a future fifth
// detail table then only needs its alias added here and in activityJoins.
func detailCoalesce(col, fallback string) string {
	expr := "rd." + col + ", wd." + col + ", cd." + col + ", od." + col + ", hd." + col
	if fallback != "" {
		expr += ", " + fallback
	}
	return "COALESCE(" + expr + ")"
}

// activityColumns is the canonical select list, kept in one place so the
// scan order can't drift between queries. Must be paired with activityJoins.
var activityColumns = `
	a.id, a.user_id, a.activity_type, a.ingest_source, COALESCE(a.source_activity_id, ''),
	a.start_time, a.name, a.notes,
	` + detailCoalesce("distance_meters", "0") + `,
	COALESCE(a.duration_seconds, 0),
	` + detailCoalesce("avg_pace_sec_per_km", "") + `,
	` + detailCoalesce("best_pace_sec_per_km", "") + `,
	a.avg_heart_rate_bpm, a.max_heart_rate_bpm, a.total_calories,
	` + detailCoalesce("elevation_gain_meters", "") + `,
	COALESCE(a.tcx_s3_key, ''), a.created_at, a.deleted_at,
	` + detailCoalesce("environment", "'outdoor'") + `,
	` + detailCoalesce("raw_distance_meters", "0") + `,
	` + detailCoalesce("elevation_loss_meters", "") + `,
	` + detailCoalesce("elevation_high_meters", "") + `,
	` + detailCoalesce("elevation_low_meters", "")

// detailTable maps an activity type to its detail table, or "" for base-only
// types (strength_training keeps its details in activity_exercises/sets,
// owned by the strength repository).
func detailTable(t ActivityType) string {
	switch t {
	case ActivityRunning:
		return "activity_run_details"
	case ActivityWalking:
		return "activity_walk_details"
	case ActivityCycling:
		return "activity_cycle_details"
	case ActivityHiking:
		return "activity_hike_details"
	case ActivityOther:
		return "activity_other_details"
	}
	return ""
}

type SQLiteRepository struct {
	db       *sql.DB
	archiver Archiver
	now      func() time.Time
}

func NewSQLiteRepository(db *sql.DB, archiver Archiver) *SQLiteRepository {
	return &SQLiteRepository{db: db, archiver: archiver, now: time.Now}
}

// Create inserts the activity + its detail row + trackpoints in one
// transaction and archives the TCX file. The ordering matters for consistency:
//
//	BEGIN → INSERT activity (+ detail + trackpoints) → archive Put → COMMIT
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
	// environment is a NOT NULL detail column with a CHECK (outdoor|indoor);
	// an unset Environment ("") would fail that CHECK. The summarizers always
	// set it, but hand-built Activities (and older test seeds) may not —
	// default to outdoor, matching the migration's per-row default.
	if a.Environment == "" {
		a.Environment = EnvironmentOutdoor
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback is a no-op once Commit succeeds; safe to always defer.
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO activities (
			id, user_id, activity_type, start_time, duration_seconds, name,
			avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
			ingest_source, source_activity_id, tcx_s3_key, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)
	`, a.ID, a.UserID, a.ActivityType, a.StartTime, a.DurationSeconds, a.Name,
		a.AvgHeartRateBpm, a.MaxHeartRateBpm, a.TotalCalories,
		a.IngestSource, a.SourceActivityID, a.TCXS3Key, a.CreatedAt, a.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return err
	}

	if table := detailTable(a.ActivityType); table != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+table+` (
				activity_id, distance_meters, raw_distance_meters,
				avg_pace_sec_per_km, best_pace_sec_per_km, elevation_gain_meters,
				elevation_loss_meters, elevation_high_meters, elevation_low_meters,
				environment, route_geojson
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, a.ID, a.DistanceMeters, a.RawDistanceMeters,
			a.AvgPaceSecPerKm, a.BestPaceSecPerKm, a.ElevationGainMeters,
			a.ElevationLossMeters, a.ElevationHighMeters, a.ElevationLowMeters,
			a.Environment, a.RouteGeoJSON); err != nil {
			return err
		}
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

// CreateManual inserts a manually-logged session's base row. Unlike Create
// there is no source file: no archive, no trackpoints, no dedup key (manual
// rows carry a NULL source_activity_id, exempt from the unique index), and
// no detail row — the unified handler persists typed details through the
// type's DetailStore afterwards. duration_seconds stays NULL when the
// request carries none.
func (r *SQLiteRepository) CreateManual(ctx context.Context, userID string, req CreateRequest) (string, error) {
	now := r.now().UTC()
	activityID := id.New()
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO activities (
			id, user_id, activity_type, start_time, duration_seconds, name, notes,
			avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
			ingest_source, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?)
	`, activityID, userID, req.Type, req.StartTime.UTC(), req.DurationSeconds, req.Name, req.Notes,
		req.AvgHeartRateBpm, req.MaxHeartRateBpm, req.TotalCalories,
		IngestManual, now, now); err != nil {
		return "", err
	}
	return activityID, nil
}

// UpdateBase full-replaces the user-editable base columns of a live owned
// row. Provenance (ingest_source, source_activity_id, tcx_s3_key) and
// created_at are never touched.
func (r *SQLiteRepository) UpdateBase(ctx context.Context, userID, activityID string, req CreateRequest) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activities
		SET start_time = ?, duration_seconds = ?, name = NULLIF(?, ''), notes = NULLIF(?, ''),
		    avg_heart_rate_bpm = ?, max_heart_rate_bpm = ?, total_calories = ?,
		    updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, req.StartTime.UTC(), req.DurationSeconds, req.Name, req.Notes,
		req.AvgHeartRateBpm, req.MaxHeartRateBpm, req.TotalCalories,
		r.now().UTC(), activityID, userID)
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

// AttachStrengthEnrichment folds a parsed strength TCX into an existing live
// strength_training base row (a logged workout): vitals, tcx provenance,
// duration when the workout has none, HR trackpoints keyed by the workout's
// own id, and the S3 archive. Returns ErrNotFound when no live
// strength_training row matches (id + user), ErrDuplicate when the
// (user, manual_tcx, source_activity_id) dedup fires, ErrStorage when the
// archive Put fails. On success a.ID and a.TCXS3Key are populated.
func (r *SQLiteRepository) AttachStrengthEnrichment(ctx context.Context, userID, activityID string, a *Activity, tcx []byte) error {
	key, err := buildTCXKey(userID, ActivityStrengthTraining, a.StartTime, activityID)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()
	// duration_seconds = COALESCE(existing, TCX): a workout the user gave an
	// end time keeps it; an end-less one adopts the TCX session duration.
	res, err := tx.ExecContext(ctx, `
		UPDATE activities
		SET avg_heart_rate_bpm = ?, max_heart_rate_bpm = ?, total_calories = ?,
		    ingest_source = ?, source_activity_id = NULLIF(?, ''), tcx_s3_key = ?,
		    duration_seconds = COALESCE(duration_seconds, ?), updated_at = ?
		WHERE id = ? AND user_id = ? AND activity_type = ? AND deleted_at IS NULL
	`, a.AvgHeartRateBpm, a.MaxHeartRateBpm, a.TotalCalories,
		IngestManualTCX, a.SourceActivityID, key,
		a.DurationSeconds, now,
		activityID, userID, ActivityStrengthTraining)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}

	// Replace-then-insert keeps a re-attach after a detach idempotent even if
	// stray points survived.
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_trackpoints WHERE activity_id = ?`, activityID); err != nil {
		return err
	}
	if err := insertTrackpointsTx(ctx, tx, activityID, a.Trackpoints); err != nil {
		return err
	}

	// Archive before COMMIT so a storage failure aborts the whole write,
	// mirroring Create.
	if err := r.archiver.Put(ctx, key, tcx, ObjectMetadata{IngestSource: IngestManualTCX}); err != nil {
		return fmt.Errorf("%w: %w", ErrStorage, err)
	}
	if err := tx.Commit(); err != nil {
		_ = r.archiver.Delete(ctx, key)
		return err
	}
	a.ID = activityID
	a.TCXS3Key = key
	return nil
}

// DetachStrengthEnrichment clears a strength row's TCX enrichment: vitals,
// provenance, and trackpoints. The archived S3 object is intentionally
// retained (matching run soft-delete semantics). duration_seconds is left
// alone — the workout's timing survives a detach. Returns ErrNotFound when
// no live enriched strength row matches.
func (r *SQLiteRepository) DetachStrengthEnrichment(ctx context.Context, userID, activityID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE activities
		SET avg_heart_rate_bpm = NULL, max_heart_rate_bpm = NULL, total_calories = NULL,
		    ingest_source = ?, source_activity_id = NULL, tcx_s3_key = NULL, updated_at = ?
		WHERE id = ? AND user_id = ? AND activity_type = ? AND deleted_at IS NULL
		  AND tcx_s3_key IS NOT NULL
	`, IngestManual, now, activityID, userID, ActivityStrengthTraining)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_trackpoints WHERE activity_id = ?`, activityID); err != nil {
		return err
	}
	return tx.Commit()
}

func insertTrackpointsTx(ctx context.Context, tx *sql.Tx, activityID string, points []Trackpoint) error {
	if len(points) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO activity_trackpoints (
			activity_id, sequence, elapsed_seconds, distance_meters,
			heart_rate_bpm, pace_sec_per_km, elevation_meters, latitude, longitude
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range points {
		if _, err := stmt.ExecContext(ctx, activityID, p.Sequence, p.ElapsedSeconds,
			p.DistanceMeters, p.HeartRateBpm, p.PaceSecPerKm, p.ElevationMeters,
			p.Latitude, p.Longitude); err != nil {
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
		INSERT INTO activity_best_efforts (activity_id, distance_key, duration_seconds, window_start_elapsed_seconds, window_end_elapsed_seconds)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range efforts {
		if _, err := stmt.ExecContext(ctx, activityID, e.DistanceKey, e.DurationSeconds, e.WindowStartElapsedSeconds, e.WindowEndElapsedSeconds); err != nil {
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
		SELECT e.activity_id, a.start_time, e.duration_seconds, rd.distance_meters,
		       rd.avg_pace_sec_per_km, e.window_start_elapsed_seconds, e.window_end_elapsed_seconds
		FROM activity_best_efforts e
		JOIN activities a ON a.id = e.activity_id
		JOIN activity_run_details rd ON rd.activity_id = a.id
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
		if err := rows.Scan(&p.ActivityID, &p.ActivityStartTime, &p.DurationSeconds, &p.ActivityDistanceMeters,
			&p.ActivityAvgPaceSecPerKm, &p.WindowStartElapsed, &p.WindowEndElapsed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BestEffortsForActivity returns the stored best-effort rows for one owned,
// live activity.
//
// Repository.Get deliberately does NOT load these — the detail read is
// already fetching a trackpoint stream, and most callers never look at best
// efforts. Consumers that do want them (the calendar event body) ask for them
// explicitly rather than making every activity read pay for a join.
//
// Ownership is enforced through the activities join, so a wrong userID or a
// soft-deleted activity returns no rows rather than another user's efforts.
func (r *SQLiteRepository) BestEffortsForActivity(ctx context.Context, userID, activityID string) ([]ActivityBestEffort, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.distance_key, e.duration_seconds, e.window_start_elapsed_seconds, e.window_end_elapsed_seconds
		FROM activity_best_efforts e
		JOIN activities a ON a.id = e.activity_id
		WHERE e.activity_id = ? AND a.user_id = ? AND a.deleted_at IS NULL
	`, activityID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActivityBestEffort
	for rows.Next() {
		var be ActivityBestEffort
		if err := rows.Scan(&be.DistanceKey, &be.DurationSeconds,
			&be.WindowStartElapsedSeconds, &be.WindowEndElapsedSeconds); err != nil {
			return nil, err
		}
		out = append(out, be)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) GetBySourceActivityID(ctx context.Context, userID string, source IngestSource, sourceActivityID string) (*Activity, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+activityColumns+activityJoins+`
		WHERE a.user_id = ? AND a.ingest_source = ? AND a.source_activity_id = ? AND a.deleted_at IS NULL
	`, userID, source, sourceActivityID)
	a, err := scanActivity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// typeFilterClauses appends filter's WHERE clause (if any) to clauses/args.
// See TypeFilter: zero value filters nothing — the unified list reads every
// type, strength_training included.
func typeFilterClauses(filter TypeFilter, clauses []string, args []any) ([]string, []any) {
	if filter.Only != "" {
		clauses = append(clauses, "a.activity_type = ?")
		args = append(args, filter.Only)
	}
	return clauses, args
}

func (r *SQLiteRepository) List(ctx context.Context, userID string, limit int, before *time.Time, filter TypeFilter) ([]Activity, error) {
	args := []any{userID}
	clauses := []string{"a.user_id = ?", "a.deleted_at IS NULL"}
	clauses, args = typeFilterClauses(filter, clauses, args)
	if before != nil {
		clauses = append(clauses, "a.start_time < ?")
		args = append(args, *before)
	}
	q := `
		SELECT ` + activityColumns + activityJoins + `
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY a.start_time DESC`
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

func (r *SQLiteRepository) ListInRange(ctx context.Context, userID string, since, until *time.Time, filter TypeFilter) ([]Activity, error) {
	args := []any{userID}
	clauses := []string{"a.user_id = ?", "a.deleted_at IS NULL"}
	clauses, args = typeFilterClauses(filter, clauses, args)
	if since != nil {
		clauses = append(clauses, "a.start_time >= ?")
		args = append(args, *since)
	}
	if until != nil {
		// Half-open interval — an activity at exactly `until` belongs to
		// the next window (callers chain adjacent month boundaries).
		clauses = append(clauses, "a.start_time < ?")
		args = append(args, *until)
	}
	q := `
		SELECT ` + activityColumns + activityJoins + `
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY a.start_time DESC`
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
		SELECT `+activityColumns+activityJoins+`
		WHERE a.id = ? AND a.user_id = ? AND a.deleted_at IS NULL
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

	// Route geometry lives on the detail row and only loads on this detail
	// path. Base-only types (strength) have no detail table and no route.
	if table := detailTable(a.ActivityType); table != "" {
		var route sql.NullString
		if err := r.db.QueryRowContext(ctx, `
			SELECT route_geojson FROM `+table+` WHERE activity_id = ?
		`, activityID).Scan(&route); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if route.Valid {
			a.RouteGeoJSON = &route.String
		}
	}
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
	// No activity_type filter: callers fetch by explicit id (the workout
	// list's enrichment read included), so every live owned row qualifies.
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+activityColumns+activityJoins+`
		WHERE a.user_id = ? AND a.deleted_at IS NULL AND a.id IN (`+placeholders+`)
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
		       heart_rate_bpm, pace_sec_per_km, elevation_meters, latitude, longitude
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
			p   Trackpoint
			hr  sql.NullInt64
			pa  sql.NullFloat64
			el  sql.NullFloat64
			lat sql.NullFloat64
			lon sql.NullFloat64
		)
		if err := rows.Scan(&p.Sequence, &p.ElapsedSeconds, &p.DistanceMeters, &hr, &pa, &el, &lat, &lon); err != nil {
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
		if lat.Valid {
			v := lat.Float64
			p.Latitude = &v
		}
		if lon.Valid {
			v := lon.Float64
			p.Longitude = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) Rename(ctx context.Context, userID, activityID, name string) (*Activity, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activities
		SET name = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, name, r.now().UTC(), activityID, userID)
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
	return r.readActivitySummary(ctx, userID, activityID)
}

// UpdateNotes writes the base row's free-text note. NULLIF collapses an empty
// note to NULL, matching how CreateManual/UpdateBase store a blank one.
func (r *SQLiteRepository) UpdateNotes(ctx context.Context, userID, activityID, notes string) (*Activity, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activities
		SET notes = NULLIF(?, ''), updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, notes, r.now().UTC(), activityID, userID)
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
	return r.readActivitySummary(ctx, userID, activityID)
}

func (r *SQLiteRepository) Calibrate(ctx context.Context, userID, activityID string, newDistanceMeters float64) (*Activity, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		actType     ActivityType
		curDistance float64
		duration    int
	)
	row := tx.QueryRowContext(ctx, `
		SELECT a.activity_type,
		       `+detailCoalesce("distance_meters", "0")+`,
		       COALESCE(a.duration_seconds, 0)
		`+activityJoins+`
		WHERE a.id = ? AND a.user_id = ? AND a.deleted_at IS NULL
	`, activityID, userID)
	if err := row.Scan(&actType, &curDistance, &duration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if curDistance <= 0 {
		// Nothing to scale from; the handler also guards this, but a zero here
		// would divide by zero below.
		return nil, fmt.Errorf("activity: cannot calibrate zero-distance activity %s", activityID)
	}
	table := detailTable(actType)
	if table == "" {
		// Base-only types carry no distance to calibrate; unreachable through
		// the handler's running-only guard.
		return nil, ErrNotFound
	}
	f := newDistanceMeters / curDistance

	// avg pace is recomputed from the corrected distance. Best pace is
	// recomputed below from the rescaled trackpoints rather than scaled by
	// ÷f here.
	avgPace := float64(duration) / (newDistanceMeters / 1000)
	if _, err := tx.ExecContext(ctx, `
		UPDATE `+table+`
		SET distance_meters = ?,
		    avg_pace_sec_per_km = ?
		WHERE activity_id = ?
	`, newDistanceMeters, avgPace, activityID); err != nil {
		return nil, err
	}

	// Rescale every trackpoint's cumulative distance and (non-null) pace. A
	// NULL pace_sec_per_km divided by f is NULL, so stationary-filtered points
	// keep their null.
	if _, err := tx.ExecContext(ctx, `
		UPDATE activity_trackpoints
		SET distance_meters = distance_meters * ?,
		    pace_sec_per_km = pace_sec_per_km / ?
		WHERE activity_id = ?
	`, f, f, activityID); err != nil {
		return nil, err
	}

	// Recompute best pace from the rescaled trackpoints rather than scaling
	// the old value by ÷f: uniform scaling moves which window is fastest, so
	// ÷f is only an approximation. The rolling window runs over the stored
	// (downsampled) points — the same stream every read-time surface uses,
	// which is what lets the detail invariant gate compare them.
	rows, queryErr := tx.QueryContext(ctx, `
		SELECT elapsed_seconds, distance_meters
		FROM activity_trackpoints
		WHERE activity_id = ?
		ORDER BY sequence
	`, activityID)
	if queryErr != nil {
		return nil, queryErr
	}
	defer rows.Close()
	var tps []Trackpoint
	for rows.Next() {
		var tp Trackpoint
		if scanErr := rows.Scan(&tp.ElapsedSeconds, &tp.DistanceMeters); scanErr != nil {
			return nil, scanErr
		}
		tps = append(tps, tp)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE `+table+` SET best_pace_sec_per_km = ? WHERE activity_id = ?
	`, bestRollingPace(tps, 1000), activityID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.Get(ctx, userID, activityID)
}

func (r *SQLiteRepository) ChangeEnvironment(ctx context.Context, userID, activityID string, newEnv Environment) (*Activity, error) {
	var (
		curEnv      Environment
		rawDistance float64
		distance    float64
		s3Key       string
		actType     ActivityType
	)
	row := r.db.QueryRowContext(ctx, `
		SELECT `+detailCoalesce("environment", "'outdoor'")+`,
		       `+detailCoalesce("raw_distance_meters", "0")+`,
		       `+detailCoalesce("distance_meters", "0")+`,
		       COALESCE(a.tcx_s3_key, ''), a.activity_type
		`+activityJoins+`
		WHERE a.id = ? AND a.user_id = ? AND a.deleted_at IS NULL
	`, activityID, userID)
	if err := row.Scan(&curEnv, &rawDistance, &distance, &s3Key, &actType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if curEnv == newEnv {
		return r.readActivitySummary(ctx, userID, activityID)
	}
	table := detailTable(actType)
	if table == "" {
		// Base-only types have no environment; unreachable through the
		// handler's endurance-only surface.
		return nil, ErrNotFound
	}

	// For indoor -> outdoor on a running activity, regenerate best efforts from
	// the archived raw TCX BEFORE opening the write transaction, so a slow S3
	// fetch never holds a DB write lock and a fetch/parse failure aborts the
	// whole change (rather than leaving an outdoor run with no PR rows).
	var regenerated []ActivityBestEffort
	if actType == ActivityRunning && newEnv == EnvironmentOutdoor {
		body, err := r.archiver.Get(ctx, s3Key)
		if err != nil {
			return nil, fmt.Errorf("change environment: fetch tcx: %w", err)
		}
		parsed, err := parseTCX(body)
		if err != nil {
			return nil, fmt.Errorf("change environment: parse tcx: %w", err)
		}
		if err := validate(parsed); err != nil {
			return nil, fmt.Errorf("change environment: validate tcx: %w", err)
		}
		// Apply the effective calibration factor to the raw cumulative
		// distances so regenerated efforts match the (possibly calibrated)
		// current distance. Never-calibrated runs have distance == raw => 1.0.
		scale := 1.0
		if rawDistance > 0 {
			scale = distance / rawDistance
		}
		scaled := make([]parsedTrackpoint, len(parsed.Trackpoints))
		copy(scaled, parsed.Trackpoints)
		for i := range scaled {
			scaled[i].DistanceMeters *= scale
		}
		regenerated = bestEfforts(scaled, StandardDistances)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE `+table+` SET environment = ?
		WHERE activity_id = ?
	`, newEnv, activityID); err != nil {
		return nil, err
	}

	if actType == ActivityRunning {
		switch newEnv {
		case EnvironmentIndoor:
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM activity_best_efforts WHERE activity_id = ?
			`, activityID); err != nil {
				return nil, err
			}
		case EnvironmentOutdoor:
			// Replace any existing rows, then insert the freshly swept set.
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM activity_best_efforts WHERE activity_id = ?
			`, activityID); err != nil {
				return nil, err
			}
			if err := insertBestEffortsTx(ctx, tx, activityID, regenerated); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.readActivitySummary(ctx, userID, activityID)
}

// readActivitySummary re-reads a row (without trackpoints) after a mutation,
// mirroring Rename's return shape. It does not filter deleted_at because the
// caller has just confirmed/updated a live row.
func (r *SQLiteRepository) readActivitySummary(ctx context.Context, userID, activityID string) (*Activity, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+activityColumns+activityJoins+`
		WHERE a.id = ? AND a.user_id = ?
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
	// partial index covers the base side of this query.
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.start_time, rd.distance_meters, COALESCE(a.duration_seconds, 0)
		FROM activities a
		JOIN activity_run_details rd ON rd.activity_id = a.id
		WHERE a.user_id = ? AND a.activity_type = ? AND a.deleted_at IS NULL
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
// buckets the samples into local weeks.
func (r *SQLiteRepository) ListRunningSamplesSince(ctx context.Context, userID string, since time.Time) ([]RunSample, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.start_time, rd.distance_meters
		FROM activities a
		JOIN activity_run_details rd ON rd.activity_id = a.id
		WHERE a.user_id = ? AND a.activity_type = ? AND a.deleted_at IS NULL AND a.start_time >= ?
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

// RecentHRStats summarizes the user's recent running HR history for the zone
// engine. It scans the HR samples of every non-deleted ActivityRunning row in
// the [now-window, now] window (excluding excludeActivityID), feeding them to
// hrzones.P99 for a spike-resistant reference, and counts how many of those
// runs carried at least one HR sample. CurrentRunP99 is deliberately left nil:
// the handler fills it from the run being viewed.
func (r *SQLiteRepository) RecentHRStats(ctx context.Context, userID string, window time.Duration, excludeActivityID string) (hrzones.Stats, error) {
	since := r.now().Add(-window)

	rows, err := r.db.QueryContext(ctx, `
		SELECT t.heart_rate_bpm
		FROM activity_trackpoints t
		JOIN activities a ON a.id = t.activity_id
		WHERE a.user_id = ? AND a.activity_type = ? AND a.deleted_at IS NULL
		  AND a.start_time >= ? AND a.id != ? AND t.heart_rate_bpm IS NOT NULL
	`, userID, ActivityRunning, since, excludeActivityID)
	if err != nil {
		return hrzones.Stats{}, err
	}
	defer rows.Close()

	var samples []int
	for rows.Next() {
		var hr int
		if err := rows.Scan(&hr); err != nil {
			return hrzones.Stats{}, err
		}
		samples = append(samples, hr)
	}
	if err := rows.Err(); err != nil {
		return hrzones.Stats{}, err
	}

	// Count distinct HR-bearing runs separately so the history count reflects
	// only runs that actually carry HR (the p99 alone can't distinguish runs).
	var runCount int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT t.activity_id)
		FROM activity_trackpoints t
		JOIN activities a ON a.id = t.activity_id
		WHERE a.user_id = ? AND a.activity_type = ? AND a.deleted_at IS NULL
		  AND a.start_time >= ? AND a.id != ? AND t.heart_rate_bpm IS NOT NULL
	`, userID, ActivityRunning, since, excludeActivityID).Scan(&runCount); err != nil {
		return hrzones.Stats{}, err
	}

	return hrzones.Stats{
		RecentHRSamplesP99: hrzones.P99(samples),
		HistoryRunCount:    runCount,
		CurrentRunP99:      nil,
	}, nil
}

// scanActivity reads one activities row out of a Row or Rows.
func scanActivity(s interface{ Scan(...any) error }) (*Activity, error) {
	var (
		act       Activity
		name      sql.NullString
		notes     sql.NullString
		avgPace   sql.NullFloat64
		bestPace  sql.NullFloat64
		avgHR     sql.NullInt64
		maxHR     sql.NullInt64
		calories  sql.NullInt64
		elevation sql.NullFloat64
		elevLoss  sql.NullFloat64
		elevHigh  sql.NullFloat64
		elevLow   sql.NullFloat64
		deletedAt sql.NullTime
	)
	if err := s.Scan(
		&act.ID, &act.UserID, &act.ActivityType, &act.IngestSource, &act.SourceActivityID,
		&act.StartTime, &name, &notes, &act.DistanceMeters, &act.DurationSeconds,
		&avgPace, &bestPace,
		&avgHR, &maxHR, &calories, &elevation,
		&act.TCXS3Key, &act.CreatedAt, &deletedAt, &act.Environment, &act.RawDistanceMeters,
		&elevLoss, &elevHigh, &elevLow,
	); err != nil {
		return nil, err
	}
	if name.Valid {
		v := name.String
		act.Name = &v
	}
	if notes.Valid {
		v := notes.String
		act.Notes = &v
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
	if elevLoss.Valid {
		v := elevLoss.Float64
		act.ElevationLossMeters = &v
	}
	if elevHigh.Valid {
		v := elevHigh.Float64
		act.ElevationHighMeters = &v
	}
	if elevLow.Valid {
		v := elevLow.Float64
		act.ElevationLowMeters = &v
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
