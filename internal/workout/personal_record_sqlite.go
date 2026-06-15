package workout

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// recomputePersonalRecordTx replaces the PR row and rebuilds the event
// chain for a single (user, exercise) pair within the given
// transaction. Always loads the user's full non-deleted history for
// that exercise — bounded by per-user workout count, fine at our
// scale. Called by every workout CRUD path so backdated workouts and
// edits of old workouts both produce a consistent state.
func (r *SQLiteRepository) recomputePersonalRecordTx(
	ctx context.Context,
	tx *sql.Tx,
	userID, exerciseID string,
	now time.Time,
) error {
	snapshots, err := r.loadSnapshotsForExerciseTx(ctx, tx, userID, exerciseID)
	if err != nil {
		return err
	}

	pr, events := RecomputePersonalRecord(snapshots, exerciseID)

	// Replace the existing PR row. Delete-then-insert beats a conditional
	// upsert for clarity given the row could end up gone (no qualifying
	// sets in remaining history).
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM personal_records WHERE user_id = ? AND exercise_id = ?
	`, userID, exerciseID); err != nil {
		return err
	}
	if pr != nil {
		pr.ID = id.New()
		pr.CreatedAt = now
		pr.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO personal_records (
				id, user_id, exercise_id, workout_id,
				weight, reps, unit, achieved_at,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, pr.ID, pr.UserID, pr.ExerciseID, pr.WorkoutID,
			pr.Weight, pr.Reps, string(pr.Unit), pr.AchievedAt,
			pr.CreatedAt, pr.UpdatedAt); err != nil {
			return err
		}
	}

	// Replace the event chain for this (user, exercise). Events are
	// fully derivable so wholesale replacement is correct.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM personal_record_events WHERE user_id = ? AND exercise_id = ?
	`, userID, exerciseID); err != nil {
		return err
	}
	for i := range events {
		e := &events[i]
		e.ID = id.New()
		e.CreatedAt = now
		var prevWeight any
		var prevReps any
		var prevUnit any
		if e.PreviousWeight != nil {
			prevWeight = *e.PreviousWeight
		}
		if e.PreviousReps != nil {
			prevReps = *e.PreviousReps
		}
		if e.PreviousUnit != nil {
			prevUnit = string(*e.PreviousUnit)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO personal_record_events (
				id, user_id, exercise_id, workout_id,
				weight, reps, unit,
				previous_weight, previous_reps, previous_unit,
				achieved_at, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, e.ID, e.UserID, e.ExerciseID, e.WorkoutID,
			e.Weight, e.Reps, string(e.Unit),
			prevWeight, prevReps, prevUnit,
			e.AchievedAt, e.CreatedAt); err != nil {
			return err
		}
	}

	return nil
}

// loadSnapshotsForExerciseTx returns chronologically-ordered snapshots
// of every non-deleted workout the user has logged that contains the
// given exercise. Each snapshot includes only the workout-exercise
// blocks matching this exercise — duplicate blocks (warmup + main)
// are preserved as separate entries so RecomputePersonalRecord can
// fold them.
func (r *SQLiteRepository) loadSnapshotsForExerciseTx(
	ctx context.Context,
	tx *sql.Tx,
	userID, exerciseID string,
) ([]WorkoutSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			w.id,
			w.user_id,
			w.performed_at,
			we.id AS we_id,
			we.exercise_order,
			s.reps,
			s.weight,
			s.unit,
			s.set_order
		FROM workouts w
		INNER JOIN workout_exercises we ON we.workout_id = w.id
		INNER JOIN sets s ON s.workout_exercise_id = we.id
		WHERE w.user_id = ?
		  AND w.deleted_at IS NULL
		  AND we.exercise_id = ?
		ORDER BY w.performed_at ASC, w.id, we.exercise_order, s.set_order
	`, userID, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []WorkoutSnapshot
	var curSnap *WorkoutSnapshot
	var curBlock *ExerciseSnapshot
	var curWorkoutID string
	var curWEID int64
	for rows.Next() {
		var wID, wUserID, unitStr string
		var performedAt time.Time
		var weID int64
		var exOrder, reps, setOrder int
		var weight float64
		if err := rows.Scan(&wID, &wUserID, &performedAt, &weID, &exOrder,
			&reps, &weight, &unitStr, &setOrder); err != nil {
			return nil, err
		}
		// New workout boundary.
		if curSnap == nil || wID != curWorkoutID {
			if curSnap != nil {
				snaps = append(snaps, *curSnap)
			}
			curSnap = &WorkoutSnapshot{
				ID:          wID,
				UserID:      wUserID,
				PerformedAt: performedAt,
			}
			curWorkoutID = wID
			curBlock = nil
			curWEID = 0
		}
		// New block boundary within the workout.
		if curBlock == nil || weID != curWEID {
			curSnap.Exercises = append(curSnap.Exercises, ExerciseSnapshot{
				ExerciseID: exerciseID,
			})
			curBlock = &curSnap.Exercises[len(curSnap.Exercises)-1]
			curWEID = weID
		}
		curBlock.Sets = append(curBlock.Sets, Set{
			Reps:   reps,
			Weight: weight,
			Unit:   user.WeightUnit(unitStr),
		})
	}
	if curSnap != nil {
		snaps = append(snaps, *curSnap)
	}
	return snaps, rows.Err()
}

// affectedExercisesForRecomputeTx returns the set of exercise IDs
// whose (user, exercise) PR could have been touched by a change to
// the given workout. Union of: exercises currently in the workout,
// exercises whose PR row points at this workout, exercises whose
// event rows point at this workout.
func (r *SQLiteRepository) affectedExercisesForRecomputeTx(
	ctx context.Context,
	tx *sql.Tx,
	workoutID string,
) (map[string]struct{}, error) {
	out := map[string]struct{}{}

	addRows := func(query string) error {
		rows, err := tx.QueryContext(ctx, query, workoutID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ex string
			if err := rows.Scan(&ex); err != nil {
				return err
			}
			out[ex] = struct{}{}
		}
		return rows.Err()
	}

	if err := addRows(`SELECT DISTINCT exercise_id FROM workout_exercises WHERE workout_id = ?`); err != nil {
		return nil, err
	}
	if err := addRows(`SELECT DISTINCT exercise_id FROM personal_records WHERE workout_id = ?`); err != nil {
		return nil, err
	}
	if err := addRows(`SELECT DISTINCT exercise_id FROM personal_record_events WHERE workout_id = ?`); err != nil {
		return nil, err
	}
	return out, nil
}

// ListPersonalRecords returns the user's PRs, sorted by achieved_at DESC.
func (r *SQLiteRepository) ListPersonalRecords(
	ctx context.Context,
	userID string,
) ([]PersonalRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, exercise_id, workout_id,
		       weight, reps, unit, achieved_at,
		       created_at, updated_at
		FROM personal_records
		WHERE user_id = ?
		ORDER BY achieved_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PersonalRecord
	for rows.Next() {
		var pr PersonalRecord
		var unitStr string
		if err := rows.Scan(
			&pr.ID, &pr.UserID, &pr.ExerciseID, &pr.WorkoutID,
			&pr.Weight, &pr.Reps, &unitStr, &pr.AchievedAt,
			&pr.CreatedAt, &pr.UpdatedAt,
		); err != nil {
			return nil, err
		}
		pr.Unit = user.WeightUnit(unitStr)
		out = append(out, pr)
	}
	return out, rows.Err()
}

// ListPersonalRecordEventsByWorkouts returns every event whose
// workout_id is in the given slice. Empty input → empty result. Used
// by the workout list endpoint to bulk-fetch PR events for the
// workouts on a page in a single query.
func (r *SQLiteRepository) ListPersonalRecordEventsByWorkouts(
	ctx context.Context,
	workoutIDs []string,
) ([]PersonalRecordEvent, error) {
	if len(workoutIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(workoutIDs))
	placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
	args := make([]any, len(workoutIDs))
	for i, w := range workoutIDs {
		args[i] = w
	}
	query := `
		SELECT id, user_id, exercise_id, workout_id,
		       weight, reps, unit,
		       previous_weight, previous_reps, previous_unit,
		       achieved_at, created_at
		FROM personal_record_events
		WHERE workout_id IN (` + placeholders + `)
		ORDER BY achieved_at DESC
	`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PersonalRecordEvent
	for rows.Next() {
		var e PersonalRecordEvent
		var unitStr string
		var prevWeight sql.NullFloat64
		var prevReps sql.NullInt64
		var prevUnit sql.NullString
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.ExerciseID, &e.WorkoutID,
			&e.Weight, &e.Reps, &unitStr,
			&prevWeight, &prevReps, &prevUnit,
			&e.AchievedAt, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		e.Unit = user.WeightUnit(unitStr)
		if prevWeight.Valid {
			v := prevWeight.Float64
			e.PreviousWeight = &v
		}
		if prevReps.Valid {
			v := int(prevReps.Int64)
			e.PreviousReps = &v
		}
		if prevUnit.Valid {
			v := user.WeightUnit(prevUnit.String)
			e.PreviousUnit = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetPersonalRecordEventsByIDs returns every event whose id is in the
// given slice. Empty input → empty result. Used by the timeline hydrator
// to render `pr` posts (keyed by event id) in a single batch read.
func (r *SQLiteRepository) GetPersonalRecordEventsByIDs(
	ctx context.Context,
	ids []string,
) ([]PersonalRecordEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `
		SELECT id, user_id, exercise_id, workout_id,
		       weight, reps, unit,
		       previous_weight, previous_reps, previous_unit,
		       achieved_at, created_at
		FROM personal_record_events
		WHERE id IN (` + placeholders + `)
		ORDER BY achieved_at DESC
	`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PersonalRecordEvent
	for rows.Next() {
		var e PersonalRecordEvent
		var unitStr string
		var prevWeight sql.NullFloat64
		var prevReps sql.NullInt64
		var prevUnit sql.NullString
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.ExerciseID, &e.WorkoutID,
			&e.Weight, &e.Reps, &unitStr,
			&prevWeight, &prevReps, &prevUnit,
			&e.AchievedAt, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		e.Unit = user.WeightUnit(unitStr)
		if prevWeight.Valid {
			v := prevWeight.Float64
			e.PreviousWeight = &v
		}
		if prevReps.Valid {
			v := int(prevReps.Int64)
			e.PreviousReps = &v
		}
		if prevUnit.Valid {
			v := user.WeightUnit(prevUnit.String)
			e.PreviousUnit = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BackfillPersonalRecords populates both PR tables from existing
// workouts when both are empty. Idempotent and safe to call on every
// startup — same pattern as BackfillOneRepMaxHistory.
//
// Runs in a single transaction so a partial population is impossible.
func (r *SQLiteRepository) BackfillPersonalRecords(ctx context.Context) error {
	var existing int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM personal_records`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	// Find every (user_id, exercise_id) pair that has at least one
	// logged set across non-deleted workouts. Recompute each.
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT w.user_id, we.exercise_id
		FROM workouts w
		INNER JOIN workout_exercises we ON we.workout_id = w.id
		WHERE w.deleted_at IS NULL
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pair struct{ userID, exerciseID string }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err = rows.Scan(&p.userID, &p.exerciseID); err != nil {
			return err
		}
		pairs = append(pairs, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(pairs) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()
	for _, p := range pairs {
		if err := r.recomputePersonalRecordTx(ctx, tx, p.userID, p.exerciseID, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("backfill: rebuilt personal records for %d (user, exercise) pairs", len(pairs))
	return nil
}
