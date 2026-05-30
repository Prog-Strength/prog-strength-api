package workout

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
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

	// Hydrate via the same batched helper ListByUser uses. With a
	// single-ID input the helper still issues exactly two statements
	// (exercises + sets), keeping GetByID at three statements total
	// regardless of how many exercises/sets the workout holds.
	byWorkout, err := r.loadWorkoutExercisesForWorkoutIDs(ctx, []string{w.ID})
	if err != nil {
		return nil, err
	}
	w.Exercises = byWorkout[w.ID]
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

	// Three SQL statements total per call, regardless of page size or
	// per-workout depth:
	//   1. SELECT workouts (this query)
	//   2. SELECT workout_exercises WHERE workout_id IN (...)
	//   3. SELECT sets WHERE workout_exercise_id IN (...)
	// The previous implementation issued 1 + N + N*M statements (a
	// nested query per workout, then per workout_exercise). See
	// sows/workout-list-batched-hydration.md.
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

	if len(workouts) == 0 {
		return workouts, nil
	}

	ids := make([]string, len(workouts))
	for i, w := range workouts {
		ids[i] = w.ID
	}
	byWorkout, err := r.loadWorkoutExercisesForWorkoutIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range workouts {
		workouts[i].Exercises = byWorkout[workouts[i].ID]
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

// loadWorkoutExercisesForWorkoutIDs hydrates every workout_exercise +
// its sets for the given workout IDs using two batched IN-clause
// queries. Returns a map keyed by workout_id so callers attach in
// O(N) time. Empty input → empty result.
//
// Replaces the prior pattern of one query per workout for the exercises
// + one query per workout_exercise for the sets, which produced
// 1 + N + N*M statements per page. See
// prog-strength-docs/sows/workout-list-batched-hydration.md.
//
// SQLite's default SQLITE_MAX_VARIABLE_NUMBER is 32,766 on 3.32+ (999
// on older builds). For our single-user beta we're nowhere near that
// limit even at the backfill caller, which loads every workout in one
// go. If/when that changes the IDs would need chunking, but the call
// sites today are all bounded — ListByUser by its LIMIT (max 100),
// GetByID by definition (one ID), and listAllWorkoutsForBackfill by
// total workout volume (which is small at v1 scale).
func (r *SQLiteRepository) loadWorkoutExercisesForWorkoutIDs(
	ctx context.Context,
	workoutIDs []string,
) (map[string][]WorkoutExercise, error) {
	if len(workoutIDs) == 0 {
		return map[string][]WorkoutExercise{}, nil
	}

	// Step 1: batched exercises query. Order ensures each workout's
	// exercises arrive grouped together in exercise_order so the
	// assembled slice preserves the authored order — same invariant
	// the old per-workout query guaranteed via its `ORDER BY
	// exercise_order` clause.
	weArgs := make([]any, len(workoutIDs))
	for i, id := range workoutIDs {
		weArgs[i] = id
	}
	weQuery := `
		SELECT id, workout_id, exercise_id, exercise_order, superset_group, notes
		FROM workout_exercises
		WHERE workout_id IN (` + placeholders(len(workoutIDs)) + `)
		ORDER BY workout_id, exercise_order
	`
	rows, err := r.db.QueryContext(ctx, weQuery, weArgs...)
	if err != nil {
		return nil, err
	}
	type stagedRow struct {
		we        WorkoutExercise
		weID      int64
		workoutID string
	}
	var staged []stagedRow
	for rows.Next() {
		var row stagedRow
		if err := rows.Scan(
			&row.weID, &row.workoutID,
			&row.we.ExerciseID, &row.we.Order, &row.we.SupersetGroup, &row.we.Notes,
		); err != nil {
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

	byWorkout := make(map[string][]WorkoutExercise, len(workoutIDs))
	if len(staged) == 0 {
		return byWorkout, nil
	}

	// Step 2: batched sets query for every workout_exercise we just
	// loaded. set_order ASC within each workout_exercise mirrors the
	// old getSets ordering.
	setArgs := make([]any, len(staged))
	for i, s := range staged {
		setArgs[i] = s.weID
	}
	setRows, err := r.db.QueryContext(ctx, `
		SELECT workout_exercise_id, reps, weight, unit
		FROM sets
		WHERE workout_exercise_id IN (`+placeholders(len(staged))+`)
		ORDER BY workout_exercise_id, set_order
	`, setArgs...)
	if err != nil {
		return nil, err
	}
	setsByWE := make(map[int64][]Set, len(staged))
	for setRows.Next() {
		var weID int64
		var s Set
		var unit string
		if err := setRows.Scan(&weID, &s.Reps, &s.Weight, &unit); err != nil {
			setRows.Close()
			return nil, err
		}
		s.Unit = user.WeightUnit(unit)
		setsByWE[weID] = append(setsByWE[weID], s)
	}
	if err := setRows.Err(); err != nil {
		setRows.Close()
		return nil, err
	}
	setRows.Close()

	// Step 3: assemble. Slice append preserves the SQL ORDER BY
	// (workout_id, exercise_order), so each workout's bucket lands in
	// authored order without an extra sort pass.
	for _, row := range staged {
		row.we.Sets = setsByWE[row.weID]
		byWorkout[row.workoutID] = append(byWorkout[row.workoutID], row.we)
	}
	return byWorkout, nil
}

// placeholders returns "?,?,?" with n question marks for use in an
// IN-clause. Caller is responsible for matching the argument slice
// length to n.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
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

	if len(workouts) == 0 {
		return workouts, nil
	}
	ids := make([]string, len(workouts))
	for i, w := range workouts {
		ids[i] = w.ID
	}
	byWorkout, err := r.loadWorkoutExercisesForWorkoutIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range workouts {
		workouts[i].Exercises = byWorkout[workouts[i].ID]
	}
	return workouts, nil
}
