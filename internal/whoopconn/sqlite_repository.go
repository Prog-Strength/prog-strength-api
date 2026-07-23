package whoopconn

import (
	"context"
	"database/sql"
	"time"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is the production implementation backed by the
// user_whoop_connection table. Token enc/nonce map to BLOB columns.
type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) Upsert(ctx context.Context, userID string, whoopUserID int64, tokens TokenBundle, scopes string, now time.Time) error {
	now = now.UTC()
	// ON CONFLICT keeps the insert/replace race-free. connected_at is set on
	// the first insert and intentionally NOT touched by the UPDATE clause, so
	// re-connecting (even after a revoke/error) preserves the original
	// timestamp while resetting status back to connected.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_whoop_connection (
			user_id, whoop_user_id,
			access_token_enc, access_token_nonce,
			refresh_token_enc, refresh_token_nonce,
			token_expires_at, scopes, status, connected_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'connected', ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			whoop_user_id       = excluded.whoop_user_id,
			access_token_enc    = excluded.access_token_enc,
			access_token_nonce  = excluded.access_token_nonce,
			refresh_token_enc   = excluded.refresh_token_enc,
			refresh_token_nonce = excluded.refresh_token_nonce,
			token_expires_at    = excluded.token_expires_at,
			scopes              = excluded.scopes,
			status              = 'connected',
			updated_at          = excluded.updated_at
	`,
		userID, whoopUserID,
		tokens.AccessTokenEnc, tokens.AccessTokenNonce,
		tokens.RefreshTokenEnc, tokens.RefreshTokenNonce,
		tokens.ExpiresAt.UTC(), scopes, now, now,
	)
	return err
}

func (r *SQLiteRepository) Get(ctx context.Context, userID string) (*Connection, error) {
	return r.scanConnection(r.db.QueryRowContext(ctx, `
		SELECT user_id, whoop_user_id, scopes, status, token_expires_at, connected_at, updated_at
		FROM user_whoop_connection
		WHERE user_id = ?
	`, userID))
}

func (r *SQLiteRepository) GetByWhoopUserID(ctx context.Context, whoopUserID int64) (*Connection, error) {
	return r.scanConnection(r.db.QueryRowContext(ctx, `
		SELECT user_id, whoop_user_id, scopes, status, token_expires_at, connected_at, updated_at
		FROM user_whoop_connection
		WHERE whoop_user_id = ?
	`, whoopUserID))
}

func (r *SQLiteRepository) scanConnection(row *sql.Row) (*Connection, error) {
	var (
		c         Connection
		statusStr string
	)
	err := row.Scan(&c.UserID, &c.WhoopUserID, &c.Scopes, &statusStr, &c.TokenExpiresAt, &c.ConnectedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Status = Status(statusStr)
	return &c, nil
}

func (r *SQLiteRepository) GetTokens(ctx context.Context, userID string) (*TokenBundle, error) {
	var t TokenBundle
	err := r.db.QueryRowContext(ctx, `
		SELECT access_token_enc, access_token_nonce, refresh_token_enc, refresh_token_nonce, token_expires_at
		FROM user_whoop_connection
		WHERE user_id = ?
	`, userID).Scan(&t.AccessTokenEnc, &t.AccessTokenNonce, &t.RefreshTokenEnc, &t.RefreshTokenNonce, &t.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *SQLiteRepository) UpdateTokens(ctx context.Context, userID string, tokens TokenBundle, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_whoop_connection
		SET access_token_enc    = ?,
		    access_token_nonce  = ?,
		    refresh_token_enc   = ?,
		    refresh_token_nonce = ?,
		    token_expires_at    = ?,
		    updated_at          = ?
		WHERE user_id = ?
	`,
		tokens.AccessTokenEnc, tokens.AccessTokenNonce,
		tokens.RefreshTokenEnc, tokens.RefreshTokenNonce,
		tokens.ExpiresAt.UTC(), now.UTC(), userID,
	)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

func (r *SQLiteRepository) SetStatus(ctx context.Context, userID string, status Status, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_whoop_connection
		SET status = ?, updated_at = ?
		WHERE user_id = ?
	`, string(status), now.UTC(), userID)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

func (r *SQLiteRepository) Revoke(ctx context.Context, userID string, now time.Time) error {
	// Token columns are NOT NULL, so wipe them to empty blobs rather than NULL.
	// token_expires_at is reset to the zero time; reads of a revoked row never
	// decrypt anyway.
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_whoop_connection
		SET status              = 'revoked',
		    access_token_enc    = ?,
		    access_token_nonce  = ?,
		    refresh_token_enc   = ?,
		    refresh_token_nonce = ?,
		    token_expires_at    = ?,
		    updated_at          = ?
		WHERE user_id = ?
	`, []byte{}, []byte{}, []byte{}, []byte{}, time.Time{}.UTC(), now.UTC(), userID)
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

func (r *SQLiteRepository) Exists(ctx context.Context, userID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `
		SELECT 1 FROM user_whoop_connection WHERE user_id = ?
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
