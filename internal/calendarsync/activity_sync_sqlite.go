package calendarsync

import (
	"context"
	"database/sql"
	"time"
)

var _ ActivitySyncRepository = (*SQLiteActivitySyncRepository)(nil)

// SQLiteActivitySyncRepository is the production ActivitySyncRepository over
// the activity_calendar_sync table (migration 053).
type SQLiteActivitySyncRepository struct {
	db *sql.DB
}

func NewSQLiteActivitySyncRepository(db *sql.DB) *SQLiteActivitySyncRepository {
	return &SQLiteActivitySyncRepository{db: db}
}

func (r *SQLiteActivitySyncRepository) Get(ctx context.Context, userID, activityID string) (*ActivitySyncState, error) {
	var (
		s      ActivitySyncState
		status string
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT activity_id, user_id, google_event_id, sync_status, last_sync_error, attempts, updated_at
		FROM activity_calendar_sync
		WHERE user_id = ? AND activity_id = ?
	`, userID, activityID).Scan(
		&s.ActivityID, &s.UserID, &s.GoogleEventID, &status, &s.LastError, &s.Attempts, &s.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrSyncStateNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Status = SyncStatus(status)
	return &s, nil
}

// MarkSynced upserts a successful write. attempts resets to 0 so a row that
// failed a few times and then recovered is not left one boot away from the
// reconciler's give-up cap.
func (r *SQLiteActivitySyncRepository) MarkSynced(ctx context.Context, userID, activityID, eventID string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO activity_calendar_sync (
			activity_id, user_id, google_event_id, sync_status, last_sync_error, attempts, updated_at
		) VALUES (?, ?, ?, 'synced', NULL, 0, ?)
		ON CONFLICT(activity_id) DO UPDATE SET
			google_event_id = excluded.google_event_id,
			sync_status     = 'synced',
			last_sync_error = NULL,
			attempts        = 0,
			updated_at      = excluded.updated_at
	`, activityID, userID, eventID, now.UTC())
	return err
}

// MarkFailed upserts a failed write. attempts increments on the existing row
// (starting at 1 for a first-attempt failure).
//
// google_event_id uses COALESCE so a failed PATCH cannot erase the id of an
// event that still exists at Google: passing nil means "no new information",
// not "there is no event". Without this, one transient 500 would orphan a live
// event and the next sync would insert a duplicate alongside it.
func (r *SQLiteActivitySyncRepository) MarkFailed(ctx context.Context, userID, activityID string, eventID *string, cause string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO activity_calendar_sync (
			activity_id, user_id, google_event_id, sync_status, last_sync_error, attempts, updated_at
		) VALUES (?, ?, ?, 'failed', ?, 1, ?)
		ON CONFLICT(activity_id) DO UPDATE SET
			google_event_id = COALESCE(excluded.google_event_id, activity_calendar_sync.google_event_id),
			sync_status     = 'failed',
			last_sync_error = excluded.last_sync_error,
			attempts        = activity_calendar_sync.attempts + 1,
			updated_at      = excluded.updated_at
	`, activityID, userID, eventID, cause, now.UTC())
	return err
}

// Release drops the stored event id and returns the row to pending. It is an
// upsert for symmetry, but the insert branch is close to unreachable in
// practice (releasing something never synced).
func (r *SQLiteActivitySyncRepository) Release(ctx context.Context, userID, activityID string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO activity_calendar_sync (
			activity_id, user_id, google_event_id, sync_status, last_sync_error, attempts, updated_at
		) VALUES (?, ?, NULL, 'pending', NULL, 0, ?)
		ON CONFLICT(activity_id) DO UPDATE SET
			google_event_id = NULL,
			sync_status     = 'pending',
			last_sync_error = NULL,
			updated_at      = excluded.updated_at
	`, activityID, userID, now.UTC())
	return err
}

// PendingSince selects the activities that still owe a Google write.
//
// The three conditions in the WHERE clause are the whole "no backfill" policy
// expressed as SQL:
//
//   - c.status = 'connected' — a revoked connection cannot write, and must not
//     accumulate retries against a grant that no longer exists.
//   - a.created_at > c.connected_at — the cutoff. Note it is created_at, NOT
//     start_time: the rule is "everything you LOG from now on", so a run you
//     did yesterday but recorded after connecting still lands on the calendar
//     (on yesterday's date, correctly), while your entire pre-existing history
//     stays untouched.
//   - no synced row, and attempts under the cap — never-attempted rows are
//     absent from the table entirely, and permanently-poisoned ones stop
//     being retried rather than burning a Google write quota on every boot.
//
// Oldest-first ordering makes a backlog larger than limit drain in a stable
// order across boots instead of re-attempting the same head repeatedly.
func (r *SQLiteActivitySyncRepository) PendingSince(ctx context.Context, maxAttempts, limit int) ([]PendingSync, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.user_id
		FROM activities a
		JOIN user_calendar_connection c ON c.user_id = a.user_id
		LEFT JOIN activity_calendar_sync s ON s.activity_id = a.id
		WHERE a.deleted_at IS NULL
		  AND c.status = 'connected'
		  AND a.created_at > c.connected_at
		  AND (s.activity_id IS NULL OR (s.sync_status != 'synced' AND s.attempts < ?))
		ORDER BY a.created_at
		LIMIT ?
	`, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PendingSync
	for rows.Next() {
		var p PendingSync
		if err := rows.Scan(&p.ActivityID, &p.UserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
