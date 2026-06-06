package running

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
	"github.com/mattn/go-sqlite3"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// sessionColumns is the canonical select list, kept in one place so the
// scan order can't drift between queries.
const sessionColumns = `
	id, user_id, garmin_activity_id, start_time, name,
	distance_meters, duration_seconds, avg_pace_sec_per_km, best_pace_sec_per_km,
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

// Create inserts the session + its trackpoints in one transaction and
// archives the TCX file. The ordering matters for consistency:
//
//	BEGIN → INSERT session (+ trackpoints) → archive Put → COMMIT
//
// The row + its points go in first so a UNIQUE violation short-circuits
// before we touch S3. The Put happens before COMMIT so a storage failure
// rolls the whole thing back — we never persist a session whose file
// isn't in the bucket. If COMMIT itself fails after a successful Put, we
// best-effort Delete the orphaned object.
func (r *SQLiteRepository) Create(ctx context.Context, s *Session, tcx []byte) error {
	now := r.now().UTC()
	s.ID = id.New()
	s.TCXS3Key = fmt.Sprintf("runs/%s/%s.tcx", s.UserID, s.ID)
	s.CreatedAt = now
	s.DeletedAt = nil

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback is a no-op once Commit succeeds; safe to always defer.
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO running_sessions (
			id, user_id, garmin_activity_id, start_time, name,
			distance_meters, duration_seconds, avg_pace_sec_per_km, best_pace_sec_per_km,
			avg_heart_rate_bpm, max_heart_rate_bpm, total_calories, elevation_gain_meters,
			tcx_s3_key, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.UserID, s.GarminActivityID, s.StartTime, s.Name,
		s.DistanceMeters, s.DurationSeconds, s.AvgPaceSecPerKm, s.BestPaceSecPerKm,
		s.AvgHeartRateBpm, s.MaxHeartRateBpm, s.TotalCalories, s.ElevationGainMeters,
		s.TCXS3Key, s.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return err
	}

	if err := insertTrackpointsTx(ctx, tx, s.ID, s.Trackpoints); err != nil {
		return err
	}

	// Archive before COMMIT so a storage failure aborts the whole write.
	if err := r.archiver.Put(ctx, s.TCXS3Key, tcx); err != nil {
		return fmt.Errorf("%w: %v", ErrStorage, err)
	}

	if err := tx.Commit(); err != nil {
		// The object is in S3 but the row didn't land — clean up so we
		// don't leak an orphan. Best-effort; ignore the delete error.
		_ = r.archiver.Delete(ctx, s.TCXS3Key)
		return err
	}
	return nil
}

func insertTrackpointsTx(ctx context.Context, tx *sql.Tx, sessionID string, points []Trackpoint) error {
	if len(points) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO running_trackpoints (
			session_id, sequence, elapsed_seconds, distance_meters,
			heart_rate_bpm, pace_sec_per_km, elevation_meters
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range points {
		if _, err := stmt.ExecContext(ctx, sessionID, p.Sequence, p.ElapsedSeconds,
			p.DistanceMeters, p.HeartRateBpm, p.PaceSecPerKm, p.ElevationMeters); err != nil {
			return err
		}
	}
	return nil
}

func (r *SQLiteRepository) GetByGarminActivityID(ctx context.Context, userID, garminActivityID string) (*Session, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+sessionColumns+`
		FROM running_sessions
		WHERE user_id = ? AND garmin_activity_id = ? AND deleted_at IS NULL
	`, userID, garminActivityID)
	s, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return s, err
}

func (r *SQLiteRepository) List(ctx context.Context, userID string, limit int, before *time.Time) ([]Session, error) {
	args := []any{userID}
	clauses := []string{"user_id = ?", "deleted_at IS NULL"}
	if before != nil {
		clauses = append(clauses, "start_time < ?")
		args = append(args, *before)
	}
	q := `
		SELECT ` + sessionColumns + `
		FROM running_sessions
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

	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) ListInRange(ctx context.Context, userID string, since, until *time.Time) ([]Session, error) {
	args := []any{userID}
	clauses := []string{"user_id = ?", "deleted_at IS NULL"}
	if since != nil {
		clauses = append(clauses, "start_time >= ?")
		args = append(args, *since)
	}
	if until != nil {
		// Half-open interval — a session at exactly `until` belongs to
		// the next window (callers chain adjacent month boundaries).
		clauses = append(clauses, "start_time < ?")
		args = append(args, *until)
	}
	q := `
		SELECT ` + sessionColumns + `
		FROM running_sessions
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY start_time DESC`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) Get(ctx context.Context, userID, sessionID string) (*Session, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+sessionColumns+`
		FROM running_sessions
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, sessionID, userID)
	s, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	points, err := r.loadTrackpoints(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	s.Trackpoints = points
	return s, nil
}

func (r *SQLiteRepository) loadTrackpoints(ctx context.Context, sessionID string) ([]Trackpoint, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT sequence, elapsed_seconds, distance_meters,
		       heart_rate_bpm, pace_sec_per_km, elevation_meters
		FROM running_trackpoints
		WHERE session_id = ?
		ORDER BY sequence ASC
	`, sessionID)
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

func (r *SQLiteRepository) Rename(ctx context.Context, userID, sessionID, name string) (*Session, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE running_sessions
		SET name = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, name, sessionID, userID)
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
	// Re-read so the returned session reflects exactly what's persisted.
	row := r.db.QueryRowContext(ctx, `
		SELECT `+sessionColumns+`
		FROM running_sessions
		WHERE id = ? AND user_id = ?
	`, sessionID, userID)
	return scanSession(row)
}

func (r *SQLiteRepository) SoftDelete(ctx context.Context, userID, sessionID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE running_sessions
		SET deleted_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, sessionID, userID)
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

func (r *SQLiteRepository) Metrics(ctx context.Context, userID string, now time.Time, loc *time.Location) (Metrics, error) {
	// Pull the minimal projection for every live session and aggregate in
	// Go. Week/month boundaries are user-local; SQLite's date() modifier
	// only takes a fixed UTC offset and is wrong across DST, so the
	// bucketing can't live in SQL. Personal-scale data makes the full
	// scan cheap and the code far clearer than window SQL.
	rows, err := r.db.QueryContext(ctx, `
		SELECT start_time, distance_meters, duration_seconds
		FROM running_sessions
		WHERE user_id = ? AND deleted_at IS NULL
	`, userID)
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

// scanSession reads one running_sessions row out of a Row or Rows.
func scanSession(s interface{ Scan(...any) error }) (*Session, error) {
	var (
		sess      Session
		name      sql.NullString
		bestPace  sql.NullFloat64
		avgHR     sql.NullInt64
		maxHR     sql.NullInt64
		calories  sql.NullInt64
		elevation sql.NullFloat64
		deletedAt sql.NullTime
	)
	if err := s.Scan(
		&sess.ID, &sess.UserID, &sess.GarminActivityID, &sess.StartTime, &name,
		&sess.DistanceMeters, &sess.DurationSeconds, &sess.AvgPaceSecPerKm, &bestPace,
		&avgHR, &maxHR, &calories, &elevation,
		&sess.TCXS3Key, &sess.CreatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if name.Valid {
		v := name.String
		sess.Name = &v
	}
	if bestPace.Valid {
		v := bestPace.Float64
		sess.BestPaceSecPerKm = &v
	}
	if avgHR.Valid {
		v := int(avgHR.Int64)
		sess.AvgHeartRateBpm = &v
	}
	if maxHR.Valid {
		v := int(maxHR.Int64)
		sess.MaxHeartRateBpm = &v
	}
	if calories.Valid {
		v := int(calories.Int64)
		sess.TotalCalories = &v
	}
	if elevation.Valid {
		v := elevation.Float64
		sess.ElevationGainMeters = &v
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		sess.DeletedAt = &t
	}
	return &sess, nil
}

// isUniqueViolation reports whether err is the go-sqlite3 UNIQUE
// constraint failure raised by the (user_id, garmin_activity_id) index.
// We check the extended code first; the string fallback covers wrapped or
// non-driver errors carrying the same message.
func isUniqueViolation(err error) bool {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return se.ExtendedCode == sqlite3.ErrConstraintUnique
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
