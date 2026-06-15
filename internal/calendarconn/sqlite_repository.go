package calendarconn

import (
	"context"
	"database/sql"
	"time"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is the production implementation backed by the
// user_calendar_connection table. enc/nonce map to BLOB columns.
type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) Upsert(ctx context.Context, userID string, refreshTokenEnc, nonce []byte, calendarID, scopes string, now time.Time) error {
	now = now.UTC()
	// ON CONFLICT keeps the insert/replace race-free. connected_at is set on
	// the first insert and intentionally NOT touched by the UPDATE clause, so
	// re-connecting (even after a revoke) preserves the original timestamp
	// while resetting status back to connected.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_calendar_connection (
			user_id, refresh_token_enc, refresh_token_nonce,
			google_calendar_id, scopes, status, connected_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'connected', ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			refresh_token_enc   = excluded.refresh_token_enc,
			refresh_token_nonce = excluded.refresh_token_nonce,
			google_calendar_id  = excluded.google_calendar_id,
			scopes              = excluded.scopes,
			status              = 'connected',
			updated_at          = excluded.updated_at
	`, userID, refreshTokenEnc, nonce, calendarID, scopes, now, now)
	return err
}

func (r *SQLiteRepository) Get(ctx context.Context, userID string) (*Connection, error) {
	var (
		c         Connection
		statusStr string
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, google_calendar_id, scopes, status, connected_at, updated_at
		FROM user_calendar_connection
		WHERE user_id = ?
	`, userID).Scan(&c.UserID, &c.GoogleCalendarID, &c.Scopes, &statusStr, &c.ConnectedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Status = Status(statusStr)
	return &c, nil
}

func (r *SQLiteRepository) GetRefreshToken(ctx context.Context, userID string) (enc, nonce []byte, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT refresh_token_enc, refresh_token_nonce
		FROM user_calendar_connection
		WHERE user_id = ?
	`, userID).Scan(&enc, &nonce)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return enc, nonce, nil
}

func (r *SQLiteRepository) SetStatus(ctx context.Context, userID string, status Status, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_calendar_connection
		SET status = ?, updated_at = ?
		WHERE user_id = ?
	`, string(status), now.UTC(), userID)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

func (r *SQLiteRepository) Delete(ctx context.Context, userID string) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM user_calendar_connection WHERE user_id = ?
	`, userID)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

func (r *SQLiteRepository) Exists(ctx context.Context, userID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `
		SELECT 1 FROM user_calendar_connection WHERE user_id = ?
	`, userID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// errIfNoRows maps a zero-rows-affected result to ErrNotFound, so callers get
// a clean "absent user" signal instead of a silent no-op.
func errIfNoRows(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
