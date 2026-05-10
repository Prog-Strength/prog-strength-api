package workout

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
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

func (r *SQLiteRepository) Create(ctx context.Context, w *Workout) error {
	if err := w.Validate(); err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()
	w.ID = id.New()
	w.CreatedAt = now
	w.UpdatedAt = now

	// Insert workout.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO workouts (id, user_id, name, performed_at, ended_at, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, w.ID, w.UserID, w.Name, w.PerformedAt, w.EndedAt, w.Notes, w.CreatedAt, w.UpdatedAt)
	if err != nil {
		return err
	}

	// Insert workout exercises and sets.
	for i := range w.Exercises {
		we := &w.Exercises[i]

		// Insert workout exercise.
		result, err := tx.ExecContext(ctx, `
			INSERT INTO workout_exercises (workout_id, exercise_id, exercise_order, superset_group, notes)
			VALUES (?, ?, ?, ?, ?)
		`, w.ID, we.ExerciseID, we.Order, we.SupersetGroup, we.Notes)
		if err != nil {
			return err
		}

		workoutExerciseID, err := result.LastInsertId()
		if err != nil {
			return err
		}

		// Insert sets for this workout exercise.
		for j := range we.Sets {
			set := &we.Sets[j]
			_, err := tx.ExecContext(ctx, `
				INSERT INTO sets (workout_exercise_id, reps, weight, unit, set_order)
				VALUES (?, ?, ?, ?, ?)
			`, workoutExerciseID, set.Reps, set.Weight, set.Unit, j)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (r *SQLiteRepository) GetByID(ctx context.Context, id string) (*Workout, error) {
	var w Workout
	err := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, performed_at, ended_at, notes, created_at, updated_at, deleted_at
		FROM workouts
		WHERE id = ? AND deleted_at IS NULL
	`, id).Scan(&w.ID, &w.UserID, &w.Name, &w.PerformedAt, &w.EndedAt, &w.Notes, &w.CreatedAt, &w.UpdatedAt, &w.DeletedAt)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Load exercises and sets.
	exercises, err := r.getWorkoutExercises(ctx, id)
	if err != nil {
		return nil, err
	}
	w.Exercises = exercises

	return &w, nil
}

func (r *SQLiteRepository) ListByUser(ctx context.Context, userID string, opts ListOptions) ([]Workout, error) {
	// Build query with filters.
	query := `
		SELECT id, user_id, name, performed_at, ended_at, notes, created_at, updated_at, deleted_at
		FROM workouts
		WHERE user_id = ? AND deleted_at IS NULL
	`
	args := []interface{}{userID}

	if opts.Since != nil {
		query += " AND performed_at >= ?"
		args = append(args, *opts.Since)
	}
	if opts.Until != nil {
		query += " AND performed_at <= ?"
		args = append(args, *opts.Until)
	}

	// Order by performed_at descending (most recent first).
	query += " ORDER BY performed_at DESC"

	// Apply pagination.
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	query += " LIMIT ? OFFSET ?"
	args = append(args, limit, opts.Offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workouts []Workout
	for rows.Next() {
		var w Workout
		if err := rows.Scan(&w.ID, &w.UserID, &w.Name, &w.PerformedAt, &w.EndedAt, &w.Notes, &w.CreatedAt, &w.UpdatedAt, &w.DeletedAt); err != nil {
			return nil, err
		}

		// Load exercises and sets for this workout.
		exercises, err := r.getWorkoutExercises(ctx, w.ID)
		if err != nil {
			return nil, err
		}
		w.Exercises = exercises

		workouts = append(workouts, w)
	}

	return workouts, rows.Err()
}

func (r *SQLiteRepository) Update(ctx context.Context, w *Workout) error {
	if err := w.Validate(); err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Fetch existing workout to preserve CreatedAt.
	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT created_at
		FROM workouts
		WHERE id = ? AND deleted_at IS NULL
	`, w.ID).Scan(&createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	w.CreatedAt = createdAt
	w.UpdatedAt = r.now().UTC()

	// Update workout.
	result, err := tx.ExecContext(ctx, `
		UPDATE workouts
		SET name = ?, performed_at = ?, ended_at = ?, notes = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, w.Name, w.PerformedAt, w.EndedAt, w.Notes, w.UpdatedAt, w.ID)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}

	// Delete existing workout exercises and sets (CASCADE handles sets).
	_, err = tx.ExecContext(ctx, `
		DELETE FROM workout_exercises WHERE workout_id = ?
	`, w.ID)
	if err != nil {
		return err
	}

	// Re-insert workout exercises and sets.
	for i := range w.Exercises {
		we := &w.Exercises[i]

		result, err := tx.ExecContext(ctx, `
			INSERT INTO workout_exercises (workout_id, exercise_id, exercise_order, superset_group, notes)
			VALUES (?, ?, ?, ?, ?)
		`, w.ID, we.ExerciseID, we.Order, we.SupersetGroup, we.Notes)
		if err != nil {
			return err
		}

		workoutExerciseID, err := result.LastInsertId()
		if err != nil {
			return err
		}

		for j := range we.Sets {
			set := &we.Sets[j]
			_, err := tx.ExecContext(ctx, `
				INSERT INTO sets (workout_exercise_id, reps, weight, unit, set_order)
				VALUES (?, ?, ?, ?, ?)
			`, workoutExerciseID, set.Reps, set.Weight, set.Unit, j)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (r *SQLiteRepository) Delete(ctx context.Context, id string) error {
	now := r.now().UTC()

	result, err := r.db.ExecContext(ctx, `
		UPDATE workouts
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, now, now, id)

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

// getWorkoutExercises loads all exercises and their sets for a workout.
func (r *SQLiteRepository) getWorkoutExercises(ctx context.Context, workoutID string) ([]WorkoutExercise, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, exercise_id, exercise_order, superset_group, notes
		FROM workout_exercises
		WHERE workout_id = ?
		ORDER BY exercise_order
	`, workoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var exercises []WorkoutExercise
	for rows.Next() {
		var we WorkoutExercise
		var weID int64
		if err := rows.Scan(&weID, &we.ExerciseID, &we.Order, &we.SupersetGroup, &we.Notes); err != nil {
			return nil, err
		}

		// Load sets for this workout exercise.
		sets, err := r.getSets(ctx, weID)
		if err != nil {
			return nil, err
		}
		we.Sets = sets

		exercises = append(exercises, we)
	}

	return exercises, rows.Err()
}

// getSets loads all sets for a workout exercise.
func (r *SQLiteRepository) getSets(ctx context.Context, workoutExerciseID int64) ([]Set, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT reps, weight, unit
		FROM sets
		WHERE workout_exercise_id = ?
		ORDER BY set_order
	`, workoutExerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sets []Set
	for rows.Next() {
		var s Set
		var unit string
		if err := rows.Scan(&s.Reps, &s.Weight, &unit); err != nil {
			return nil, err
		}
		s.Unit = user.WeightUnit(unit)
		sets = append(sets, s)
	}

	return sets, rows.Err()
}
