package bodyweight

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

func (r *SQLiteRepository) Create(ctx context.Context, e *Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()
	e.ID = id.New()
	e.CreatedAt = now
	e.DeletedAt = nil

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO bodyweight_entries (
			id, user_id, weight, unit, measured_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, e.ID, e.UserID, e.Weight, string(e.Unit), e.MeasuredAt, e.CreatedAt)
	return err
}

func (r *SQLiteRepository) Get(ctx context.Context, userID, entryID string) (*Entry, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, weight, unit, measured_at, created_at, deleted_at
		FROM bodyweight_entries
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, entryID, userID)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return e, err
}

func (r *SQLiteRepository) List(ctx context.Context, userID string, since, until *time.Time) ([]Entry, error) {
	// Same branching pattern the nutrition log uses — keep each query
	// statically readable rather than building dynamic SQL with
	// placeholders. At single-user scale the duplication has no cost.
	args := []any{userID}
	clauses := []string{"user_id = ?", "deleted_at IS NULL"}
	if since != nil {
		clauses = append(clauses, "measured_at >= ?")
		args = append(args, *since)
	}
	if until != nil {
		clauses = append(clauses, "measured_at < ?")
		args = append(args, *until)
	}
	q := `
		SELECT id, user_id, weight, unit, measured_at, created_at, deleted_at
		FROM bodyweight_entries
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY measured_at DESC
	`
	rows, err := r.db.QueryContext(ctx, q, args...)
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
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) Delete(ctx context.Context, userID, entryID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE bodyweight_entries
		SET deleted_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, entryID, userID)
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

// scanner is satisfied by *sql.Row and *sql.Rows; lets the same scan
// path service both single-row Get and multi-row List loops.
type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(s scanner) (*Entry, error) {
	var (
		e         Entry
		unitStr   string
		deletedAt sql.NullTime
	)
	if err := s.Scan(
		&e.ID, &e.UserID, &e.Weight, &unitStr, &e.MeasuredAt, &e.CreatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	e.Unit = user.WeightUnit(unitStr)
	if deletedAt.Valid {
		t := deletedAt.Time
		e.DeletedAt = &t
	}
	return &e, nil
}
