package whooprecovery

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is the production implementation backed by the
// user_whoop_recovery table.
type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// Upsert inserts the (user_id, date) row or replaces its metrics in a single
// statement. ON CONFLICT keeps it race-free vs. get-then-write. id/created_at
// are supplied for the insert path; on conflict the UPDATE clause touches only
// the metrics, cycle_id, sleep_id, and updated_at, so id and created_at are
// preserved.
func (r *SQLiteRepository) Upsert(ctx context.Context, e Entry, now time.Time) error {
	now = now.UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_whoop_recovery (
			id, user_id, date, recovery_score, resting_heart_rate,
			hrv_rmssd_milli, cycle_id, sleep_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, date) DO UPDATE SET
			recovery_score     = excluded.recovery_score,
			resting_heart_rate = excluded.resting_heart_rate,
			hrv_rmssd_milli    = excluded.hrv_rmssd_milli,
			cycle_id           = excluded.cycle_id,
			sleep_id           = excluded.sleep_id,
			updated_at         = excluded.updated_at
	`,
		id.New(), e.UserID, e.Date,
		nullFloat(e.RecoveryScore), nullFloat(e.RestingHeartRate), nullFloat(e.HRVRmssdMilli),
		e.CycleID, e.SleepID, now, now,
	)
	return err
}

// ListRange returns the user's rows with since <= date <= until, both bounds
// inclusive and either "" for an open bound, newest date first.
func (r *SQLiteRepository) ListRange(ctx context.Context, userID, since, until string) ([]Entry, error) {
	args := []any{userID}
	clauses := []string{"user_id = ?"}
	if since != "" {
		clauses = append(clauses, "date >= ?")
		args = append(args, since)
	}
	if until != "" {
		clauses = append(clauses, "date <= ?")
		args = append(args, until)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, date, recovery_score, resting_heart_rate,
			hrv_rmssd_milli, cycle_id, sleep_id, created_at, updated_at
		FROM user_whoop_recovery
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY date DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteBySleepID hard-deletes the user's row with the matching sleep_id.
// A missing row is not an error (idempotent webhook delete).
func (r *SQLiteRepository) DeleteBySleepID(ctx context.Context, userID, sleepID string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM user_whoop_recovery
		WHERE user_id = ? AND sleep_id = ?
	`, userID, sleepID)
	return err
}

// nullFloat maps a *float64 to a sql.NullFloat64 so a nil pointer stores SQL
// NULL rather than a zero value.
func nullFloat(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

// scanner is satisfied by *sql.Row and *sql.Rows; lets the same scan path
// service both single-row reads and multi-row List loops.
type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(s scanner) (Entry, error) {
	var (
		e                      Entry
		recovery, resting, hrv sql.NullFloat64
	)
	if err := s.Scan(
		&e.ID, &e.UserID, &e.Date, &recovery, &resting, &hrv,
		&e.CycleID, &e.SleepID, &e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		return Entry{}, err
	}
	if recovery.Valid {
		v := recovery.Float64
		e.RecoveryScore = &v
	}
	if resting.Valid {
		v := resting.Float64
		e.RestingHeartRate = &v
	}
	if hrv.Valid {
		v := hrv.Float64
		e.HRVRmssdMilli = &v
	}
	return e, nil
}
