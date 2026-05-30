package workout

import (
	"context"
	"database/sql"
	"errors"
	"log"
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

	// Derived 1RM history. AggregateOneRepMax is pure, so the same call
	// covers create here and update below — keeping the live write path
	// in lockstep with the backfill aggregation.
	if err := r.writeOneRepMaxHistoryTx(ctx, tx, *w, now); err != nil {
		return err
	}

	// Personal records + event log. Recompute for each exercise in the
	// new workout. A backdated workout could affect history downstream
	// of itself, which is why we re-derive instead of just checking
	// "does this beat the current PR?" — see personal_record.go.
	for _, exerciseID := range ExercisesInWorkout(*w) {
		if err := r.recomputePersonalRecordTx(ctx, tx, w.UserID, exerciseID, now); err != nil {
			return err
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

	// Materialize the parent rows first, then close them, then fetch
	// nested data. Calling getWorkoutExercises inside rows.Next() would
	// hold this connection from the pool while issuing another query —
	// with enough concurrent requests every connection ends up waiting
	// on a nested call that can never acquire a free conn, and the pool
	// deadlocks. Draining to a slice releases the connection before the
	// nested fan-out begins.
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var workouts []Workout
	for rows.Next() {
		var w Workout
		if err := rows.Scan(&w.ID, &w.UserID, &w.Name, &w.PerformedAt, &w.EndedAt, &w.Notes, &w.CreatedAt, &w.UpdatedAt, &w.DeletedAt); err != nil {
			rows.Close()
			return nil, err
		}
		workouts = append(workouts, w)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range workouts {
		exercises, err := r.getWorkoutExercises(ctx, workouts[i].ID)
		if err != nil {
			return nil, err
		}
		workouts[i].Exercises = exercises
	}

	return workouts, nil
}

func (r *SQLiteRepository) CountByUser(
	ctx context.Context,
	userID string,
	opts ListOptions,
) (int, error) {
	query := `
		SELECT COUNT(*)
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
	var n int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
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

	// Replace derived 1RM history rows for this workout. Update is full-
	// replacement on the workout side, so the history rows have to be
	// regenerated from the new shape; delete-then-insert is simpler than
	// trying to compute a diff.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM exercise_one_rep_max_history WHERE workout_id = ?`, w.ID); err != nil {
		return err
	}
	now := r.now().UTC()
	if err := r.writeOneRepMaxHistoryTx(ctx, tx, *w, now); err != nil {
		return err
	}

	// PR recompute. Union the new workout's exercises with the
	// exercises whose PR rows or events still reference this workout
	// — that union covers any exercise that could have been touched
	// by the edit (added, removed, or had its sets changed).
	affected, err := r.affectedExercisesForRecomputeTx(ctx, tx, w.ID)
	if err != nil {
		return err
	}
	for _, exerciseID := range ExercisesInWorkout(*w) {
		affected[exerciseID] = struct{}{}
	}
	for exerciseID := range affected {
		if err := r.recomputePersonalRecordTx(ctx, tx, w.UserID, exerciseID, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *SQLiteRepository) Delete(ctx context.Context, workoutID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()

	// Look up the user_id before the soft delete fires; we need it to
	// recompute affected PRs after the workout is gone.
	var userID string
	err = tx.QueryRowContext(ctx,
		`SELECT user_id FROM workouts WHERE id = ? AND deleted_at IS NULL`,
		workoutID).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	}

	// Capture affected exercises BEFORE the soft delete so we don't
	// race against the PR table queries below.
	affected, err := r.affectedExercisesForRecomputeTx(ctx, tx, workoutID)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE workouts
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, now, now, workoutID)
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

	// History rows are derived and not soft-deleted — hard delete keeps
	// baseline queries from having to filter by workout state at read
	// time. Safe because the table is fully rebuildable from `workouts`.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM exercise_one_rep_max_history WHERE workout_id = ?`, workoutID); err != nil {
		return err
	}

	// PR recompute. The recompute itself reads with `w.deleted_at IS
	// NULL`, so the soft-deleted workout is correctly excluded.
	for exerciseID := range affected {
		if err := r.recomputePersonalRecordTx(ctx, tx, userID, exerciseID, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// getWorkoutExercises loads all exercises and their sets for a workout.
func (r *SQLiteRepository) getWorkoutExercises(ctx context.Context, workoutID string) ([]WorkoutExercise, error) {
	// Same pattern as ListByUser: drain the parent rows + close the
	// connection before calling getSets, so we don't hold one pool
	// connection while waiting on another.
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, exercise_id, exercise_order, superset_group, notes
		FROM workout_exercises
		WHERE workout_id = ?
		ORDER BY exercise_order
	`, workoutID)
	if err != nil {
		return nil, err
	}
	type weRow struct {
		we   WorkoutExercise
		weID int64
	}
	var staged []weRow
	for rows.Next() {
		var row weRow
		if err := rows.Scan(&row.weID, &row.we.ExerciseID, &row.we.Order, &row.we.SupersetGroup, &row.we.Notes); err != nil {
			rows.Close()
			return nil, err
		}
		staged = append(staged, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	exercises := make([]WorkoutExercise, 0, len(staged))
	for _, row := range staged {
		sets, err := r.getSets(ctx, row.weID)
		if err != nil {
			return nil, err
		}
		row.we.Sets = sets
		exercises = append(exercises, row.we)
	}
	return exercises, nil
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

// writeOneRepMaxHistoryTx inserts the derived 1RM history rows for a
// workout into the given transaction. Used by Create and Update so the
// same aggregation function services both. Stable timestamp passed in
// rather than read from r.now so create/update use a single instant.
func (r *SQLiteRepository) writeOneRepMaxHistoryTx(ctx context.Context, tx *sql.Tx, w Workout, now time.Time) error {
	for _, e := range AggregateOneRepMax(w) {
		e.ID = id.New()
		e.CreatedAt = now
		e.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO exercise_one_rep_max_history (
				id, user_id, workout_id, exercise_id, performed_at,
				min_estimated_1rm, avg_estimated_1rm, max_estimated_1rm,
				set_count, unit, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, e.ID, e.UserID, e.WorkoutID, e.ExerciseID, e.PerformedAt,
			e.MinEstimated1RM, e.AvgEstimated1RM, e.MaxEstimated1RM,
			e.SetCount, string(e.Unit), e.CreatedAt, e.UpdatedAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *SQLiteRepository) ListOneRepMaxHistory(
	ctx context.Context,
	userID, exerciseID string,
	since, until *time.Time,
) ([]OneRepMaxEntry, error) {
	query := `
		SELECT id, user_id, workout_id, exercise_id, performed_at,
		       min_estimated_1rm, avg_estimated_1rm, max_estimated_1rm,
		       set_count, unit, created_at, updated_at
		FROM exercise_one_rep_max_history
		WHERE user_id = ? AND exercise_id = ?
	`
	args := []interface{}{userID, exerciseID}
	if since != nil {
		query += " AND performed_at >= ?"
		args = append(args, *since)
	}
	if until != nil {
		query += " AND performed_at <= ?"
		args = append(args, *until)
	}
	query += " ORDER BY performed_at DESC"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OneRepMaxEntry
	for rows.Next() {
		var e OneRepMaxEntry
		var unit string
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.WorkoutID, &e.ExerciseID, &e.PerformedAt,
			&e.MinEstimated1RM, &e.AvgEstimated1RM, &e.MaxEstimated1RM,
			&e.SetCount, &unit, &e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, err
		}
		e.Unit = user.WeightUnit(unit)
		out = append(out, e)
	}
	return out, rows.Err()
}

// BackfillOneRepMaxHistory populates the 1RM history table from existing
// workouts when the table is empty. Idempotent — safe to call on every
// startup; second and subsequent calls find a non-empty table and exit.
//
// Lives in Go (rather than the SQL migration) so it can share the same
// AggregateOneRepMax function used by the live write path. That shared-
// function invariant is the load-bearing piece — without it backfilled
// rows could subtly disagree with rows written by Create/Update.
//
// Runs in a single transaction so a partial population is impossible.
func (r *SQLiteRepository) BackfillOneRepMaxHistory(ctx context.Context) error {
	var existing int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM exercise_one_rep_max_history`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	workouts, err := r.listAllWorkoutsForBackfill(ctx)
	if err != nil {
		return err
	}
	if len(workouts) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()
	inserted := 0
	for _, w := range workouts {
		for _, e := range AggregateOneRepMax(w) {
			e.ID = id.New()
			e.CreatedAt = now
			e.UpdatedAt = now
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO exercise_one_rep_max_history (
					id, user_id, workout_id, exercise_id, performed_at,
					min_estimated_1rm, avg_estimated_1rm, max_estimated_1rm,
					set_count, unit, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, e.ID, e.UserID, e.WorkoutID, e.ExerciseID, e.PerformedAt,
				e.MinEstimated1RM, e.AvgEstimated1RM, e.MaxEstimated1RM,
				e.SetCount, string(e.Unit), e.CreatedAt, e.UpdatedAt); err != nil {
				return err
			}
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("backfill: inserted %d one-rep-max history rows from %d workouts", inserted, len(workouts))
	return nil
}

// listAllWorkoutsForBackfill loads every non-deleted workout with its
// exercises and sets. Used only by BackfillOneRepMaxHistory — production
// reads go through ListByUser.
func (r *SQLiteRepository) listAllWorkoutsForBackfill(ctx context.Context) ([]Workout, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, performed_at
		FROM workouts
		WHERE deleted_at IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workouts []Workout
	for rows.Next() {
		var w Workout
		if err := rows.Scan(&w.ID, &w.UserID, &w.PerformedAt); err != nil {
			return nil, err
		}
		workouts = append(workouts, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range workouts {
		ex, err := r.getWorkoutExercises(ctx, workouts[i].ID)
		if err != nil {
			return nil, err
		}
		workouts[i].Exercises = ex
	}
	return workouts, nil
}
