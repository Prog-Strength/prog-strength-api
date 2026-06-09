package nutrition

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GetMacroGoals returns the user's daily macro targets. When the user
// has no row yet, the read collapses to a zero-valued struct with nil
// timestamps — the client interprets that as "never set" and renders
// the empty-state ring outline. We deliberately do NOT return
// ErrNotFound for missing rows: every user has implicit goals (zero
// across the board) until they write real ones.
func (r *SQLiteRepository) GetMacroGoals(
	ctx context.Context,
	userID string,
) (MacroGoals, error) {
	var (
		g                    MacroGoals
		createdAt, updatedAt time.Time
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, protein_g, carbs_g, fat_g, calories,
		       created_at, updated_at
		FROM user_macro_goals
		WHERE user_id = ?
	`, userID).Scan(
		&g.UserID,
		&g.ProteinG,
		&g.CarbsG,
		&g.FatG,
		&g.Calories,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MacroGoals{UserID: userID}, nil
	}
	if err != nil {
		return MacroGoals{}, err
	}
	g.CreatedAt = &createdAt
	g.UpdatedAt = &updatedAt
	return g, nil
}

// UpsertMacroGoals INSERTs the user's first goals row or UPDATEs the
// existing one in a single statement. SQLite's ON CONFLICT clause
// keeps the call race-free vs. the get-then-write pattern, so two
// concurrent PUTs from the same user resolve to the later writer's
// values rather than dropping one silently.
//
// `created_at` is only set on the initial insert; subsequent calls
// preserve it via the excluded.created_at-vs-current-value
// distinction (the UPDATE clause doesn't touch created_at at all).
// `updated_at` bumps on every call.
func (r *SQLiteRepository) UpsertMacroGoals(
	ctx context.Context,
	g MacroGoals,
	now time.Time,
) (MacroGoals, error) {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_macro_goals (
			user_id, protein_g, carbs_g, fat_g, calories,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			protein_g  = excluded.protein_g,
			carbs_g    = excluded.carbs_g,
			fat_g      = excluded.fat_g,
			calories   = excluded.calories,
			updated_at = excluded.updated_at
	`,
		g.UserID, g.ProteinG, g.CarbsG, g.FatG, g.Calories,
		now, now,
	)
	if err != nil {
		return MacroGoals{}, err
	}
	// Re-read so the response carries the real created_at (which the
	// initial-insert path just wrote, or the prior-write path preserved).
	return r.GetMacroGoals(ctx, g.UserID)
}
