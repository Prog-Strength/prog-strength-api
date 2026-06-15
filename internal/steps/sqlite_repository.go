package steps

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
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

// UpsertEntry inserts the (user_id, date) row or replaces its step count
// in a single statement. ON CONFLICT keeps it race-free vs. get-then-write.
// id/created_at are supplied for the insert path; on conflict the UPDATE
// clause touches only steps + updated_at, so created_at is preserved.
// Re-reads so the response carries the stored id and real timestamps.
func (r *SQLiteRepository) UpsertEntry(ctx context.Context, e *Entry) (Entry, error) {
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	now := r.now().UTC()
	newID := id.New()

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_steps (
			id, user_id, date, steps, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, date) DO UPDATE SET
			steps      = excluded.steps,
			updated_at = excluded.updated_at
	`, newID, e.UserID, e.Date, e.Steps, now, now)
	if err != nil {
		return Entry{}, err
	}
	return r.get(ctx, e.UserID, e.Date)
}

// get reads back a single (user_id, date) row. Used to return the stored
// shape after an upsert; not part of the Repository interface.
func (r *SQLiteRepository) get(ctx context.Context, userID, date string) (Entry, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, date, steps, created_at, updated_at
		FROM user_steps
		WHERE user_id = ? AND date = ?
	`, userID, date)
	return scanEntry(row)
}

func (r *SQLiteRepository) List(ctx context.Context, userID string, since, until *string, limit int, before *string) ([]Entry, string, error) {
	// Build the WHERE clause dynamically, same pattern bodyweight's List
	// uses. Keyset mode wins over range mode whenever limit is set.
	args := []any{userID}
	clauses := []string{"user_id = ?"}

	keyset := limit > 0
	if keyset {
		if before != nil {
			clauses = append(clauses, "date < ?")
			args = append(args, *before)
		}
	} else {
		if since != nil {
			clauses = append(clauses, "date >= ?")
			args = append(args, *since)
		}
		if until != nil {
			clauses = append(clauses, "date <= ?")
			args = append(args, *until)
		}
	}

	q := `
		SELECT id, user_id, date, steps, created_at, updated_at
		FROM user_steps
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY date DESC`
	if keyset {
		// Fetch one extra row isn't necessary: nextBefore is the last
		// row's date whenever the page came back full, which already
		// signals "more may exist" without an over-read.
		q += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	nextBefore := ""
	if keyset && len(out) == limit {
		nextBefore = out[len(out)-1].Date
	}
	return out, nextBefore, nil
}

func (r *SQLiteRepository) Delete(ctx context.Context, userID, date string) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM user_steps
		WHERE user_id = ? AND date = ?
	`, userID, date)
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

// GetGoal returns the user's goal, collapsing a missing row to a
// zero-valued Goal with nil timestamps (the "never set" state). See the
// Repository interface comment; mirrors bodyweight.GetBodyweightGoal.
func (r *SQLiteRepository) GetGoal(ctx context.Context, userID string) (Goal, error) {
	var (
		g                    Goal
		createdAt, updatedAt time.Time
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, goal, created_at, updated_at
		FROM user_steps_goal
		WHERE user_id = ?
	`, userID).Scan(&g.UserID, &g.Goal, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return Goal{UserID: userID}, nil
	}
	if err != nil {
		return Goal{}, err
	}
	g.CreatedAt = &createdAt
	g.UpdatedAt = &updatedAt
	return g, nil
}

// UpsertGoal INSERTs the user's first goal row or UPDATEs the existing one
// in a single statement. ON CONFLICT keeps it race-free vs. get-then-write.
// now is passed for both created_at and updated_at on insert; created_at is
// preserved on conflict because the UPDATE clause doesn't touch it. Re-reads
// so the response carries real timestamps.
func (r *SQLiteRepository) UpsertGoal(ctx context.Context, g Goal, now time.Time) (Goal, error) {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_steps_goal (
			user_id, goal, created_at, updated_at
		) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			goal       = excluded.goal,
			updated_at = excluded.updated_at
	`, g.UserID, g.Goal, now, now)
	if err != nil {
		return Goal{}, err
	}
	return r.GetGoal(ctx, g.UserID)
}

// scanner is satisfied by *sql.Row and *sql.Rows; lets the same scan path
// service both single-row get and multi-row List loops.
type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(s scanner) (Entry, error) {
	var e Entry
	if err := s.Scan(
		&e.ID, &e.UserID, &e.Date, &e.Steps, &e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		return Entry{}, err
	}
	return e, nil
}
