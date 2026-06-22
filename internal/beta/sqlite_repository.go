package beta

import (
	"context"
	"database/sql"
	"time"
)

// Compile-time check that *SQLiteRepository satisfies Repository (and so
// Checker).
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is a SQLite-backed implementation of Repository.
type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{
		db:  db,
		now: time.Now,
	}
}

// IsAllowed reports whether the email may pass the gate. An empty table
// disables the gate (everyone allowed); otherwise membership is an indexed
// primary-key lookup. Both checks are cheap SELECT EXISTS — no full scan.
func (r *SQLiteRepository) IsAllowed(ctx context.Context, email string) (bool, error) {
	hasAny, err := r.hasAny(ctx)
	if err != nil {
		return false, err
	}
	if !hasAny {
		return true, nil
	}

	var exists bool
	err = r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM beta_allowed_emails WHERE email = ?)`,
		normalizeEmail(email),
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (r *SQLiteRepository) Add(ctx context.Context, email, addedBy, note string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO beta_allowed_emails (email, added_at, added_by, note)
		VALUES (?, ?, ?, ?)
	`, normalizeEmail(email), r.now().UTC(), nullable(addedBy), nullable(note))
	return err
}

func (r *SQLiteRepository) Remove(ctx context.Context, email string) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM beta_allowed_emails WHERE email = ?`,
		normalizeEmail(email),
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (r *SQLiteRepository) List(ctx context.Context) ([]AllowedEmail, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT email, added_at, added_by, note
		FROM beta_allowed_emails
		ORDER BY added_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AllowedEmail
	for rows.Next() {
		var e AllowedEmail
		if err := rows.Scan(&e.Email, &e.AddedAt, &e.AddedBy, &e.Note); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// hasAny reports whether the table holds at least one row. Cheap (LIMIT 1
// via EXISTS), never a full scan — used both to short-circuit IsAllowed's
// allow-all branch and to gate the one-time seed.
func (r *SQLiteRepository) hasAny(ctx context.Context) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM beta_allowed_emails)`,
	).Scan(&exists)
	return exists, err
}

// nullable maps an empty string to a SQL NULL so absent added_by/note round-
// trip as nil pointers rather than empty strings.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
