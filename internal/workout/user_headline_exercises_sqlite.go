package workout

import (
	"context"
	"time"
)

// ListUserHeadlineExercises returns the user's saved headline-exercise
// selection in display order. Empty result is the "user hasn't
// customized" signal — the handler falls back to HeadlineExercises.
func (r *SQLiteRepository) ListUserHeadlineExercises(
	ctx context.Context,
	userID string,
) ([]UserHeadlineExercise, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, exercise_id, position, created_at
		FROM user_headline_exercises
		WHERE user_id = ?
		ORDER BY position ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UserHeadlineExercise
	for rows.Next() {
		var uhe UserHeadlineExercise
		if err := rows.Scan(
			&uhe.UserID, &uhe.ExerciseID, &uhe.Position, &uhe.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, uhe)
	}
	return out, rows.Err()
}

// ReplaceUserHeadlineExercises atomically replaces the user's
// selection. Caller is responsible for validating slugs against the
// exercise catalog, capping at MaxHeadlineExercises, and rejecting
// duplicates — the repo trusts its input.
//
// Implementation: delete all existing rows for user_id, then insert
// the new set with position taken from the slice index. Both
// operations are inside a single transaction so a concurrent reader
// never sees a half-applied state, and the unique index on
// (user_id, position) survives the swap because the DELETE clears
// the slots before the INSERT refills them.
func (r *SQLiteRepository) ReplaceUserHeadlineExercises(
	ctx context.Context,
	userID string,
	exerciseIDs []string,
	now time.Time,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM user_headline_exercises WHERE user_id = ?
	`, userID); err != nil {
		return err
	}

	if len(exerciseIDs) > 0 {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO user_headline_exercises (
				user_id, exercise_id, position, created_at
			) VALUES (?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for pos, exerciseID := range exerciseIDs {
			if _, err := stmt.ExecContext(ctx, userID, exerciseID, pos, now); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}
