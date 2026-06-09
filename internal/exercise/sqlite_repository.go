package exercise

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is a SQLite-backed implementation of Repository.
// The exercise catalog is read-only from the API perspective; exercises
// are seeded on startup and managed through migrations/admin tools.
type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) GetByID(ctx context.Context, id string) (*Exercise, error) {
	var ex Exercise
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, created_at, updated_at, deleted_at
		FROM exercises
		WHERE id = ? AND deleted_at IS NULL
	`, id).Scan(&ex.ID, &ex.Name, &ex.Description, &ex.CreatedAt, &ex.UpdatedAt, &ex.DeletedAt)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Hydrate via the same batched helpers List uses. Three statements
	// total: the QueryRow above, one batched muscle_groups lookup, one
	// batched equipment lookup.
	mgByExercise, err := r.loadMuscleGroupsForExerciseIDs(ctx, []string{ex.ID})
	if err != nil {
		return nil, err
	}
	eqByExercise, err := r.loadEquipmentForExerciseIDs(ctx, []string{ex.ID})
	if err != nil {
		return nil, err
	}
	ex.MuscleGroups = mgByExercise[ex.ID]
	ex.Equipment = eqByExercise[ex.ID]
	return &ex, nil
}

func (r *SQLiteRepository) List(ctx context.Context, opts ListOptions) ([]Exercise, error) {
	// Build query with optional filters.
	query := `
		SELECT DISTINCT e.id, e.name, e.description, e.created_at, e.updated_at, e.deleted_at
		FROM exercises e
	`
	var joins []string
	var conditions []string
	var args []interface{}

	conditions = append(conditions, "e.deleted_at IS NULL")

	if opts.MuscleGroup != "" {
		joins = append(joins, "JOIN exercise_muscle_groups emg ON e.id = emg.exercise_id")
		conditions = append(conditions, "emg.muscle_group = ?")
		args = append(args, string(opts.MuscleGroup))
	}

	if opts.Equipment != "" {
		joins = append(joins, "JOIN exercise_equipment ee ON e.id = ee.exercise_id")
		conditions = append(conditions, "ee.equipment = ?")
		args = append(args, string(opts.Equipment))
	}

	if len(joins) > 0 {
		query += " " + strings.Join(joins, " ")
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	// Three SQL statements total per call, regardless of catalog size:
	//   1. SELECT exercises (this query)
	//   2. SELECT exercise_muscle_groups WHERE exercise_id IN (...)
	//   3. SELECT exercise_equipment WHERE exercise_id IN (...)
	// The previous implementation issued 1 + 2N statements (one
	// muscle_groups + one equipment query per exercise). See
	// prog-strength-docs/sows/workout-list-batched-hydration.md.
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var exercises []Exercise
	for rows.Next() {
		var ex Exercise
		if err = rows.Scan(&ex.ID, &ex.Name, &ex.Description, &ex.CreatedAt, &ex.UpdatedAt, &ex.DeletedAt); err != nil {
			return nil, err
		}
		exercises = append(exercises, ex)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	if len(exercises) > 0 {
		ids := make([]string, len(exercises))
		for i, ex := range exercises {
			ids[i] = ex.ID
		}
		mgByExercise, err := r.loadMuscleGroupsForExerciseIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		eqByExercise, err := r.loadEquipmentForExerciseIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range exercises {
			exercises[i].MuscleGroups = mgByExercise[exercises[i].ID]
			exercises[i].Equipment = eqByExercise[exercises[i].ID]
		}
	}

	// Sort alphabetically by name (case-insensitive).
	sort.Slice(exercises, func(i, j int) bool {
		return strings.ToLower(exercises[i].Name) < strings.ToLower(exercises[j].Name)
	})

	return exercises, nil
}

// loadMuscleGroupsForExerciseIDs returns the muscle-group rows for the
// given exercise IDs, grouped by exercise_id, in muscle_group ASC order
// within each bucket. Empty input → empty result.
//
// Single batched query replaces the prior one-query-per-exercise
// pattern. See prog-strength-docs/sows/workout-list-batched-hydration.md.
func (r *SQLiteRepository) loadMuscleGroupsForExerciseIDs(
	ctx context.Context,
	exerciseIDs []string,
) (map[string][]MuscleGroup, error) {
	out := make(map[string][]MuscleGroup, len(exerciseIDs))
	if len(exerciseIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(exerciseIDs))
	for i, id := range exerciseIDs {
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT exercise_id, muscle_group
		FROM exercise_muscle_groups
		WHERE exercise_id IN (`+placeholders(len(exerciseIDs))+`)
		ORDER BY exercise_id, muscle_group
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var exID string
		var mg MuscleGroup
		if err := rows.Scan(&exID, &mg); err != nil {
			return nil, err
		}
		out[exID] = append(out[exID], mg)
	}
	return out, rows.Err()
}

// loadEquipmentForExerciseIDs mirrors loadMuscleGroupsForExerciseIDs
// against the exercise_equipment join table.
func (r *SQLiteRepository) loadEquipmentForExerciseIDs(
	ctx context.Context,
	exerciseIDs []string,
) (map[string][]Equipment, error) {
	out := make(map[string][]Equipment, len(exerciseIDs))
	if len(exerciseIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(exerciseIDs))
	for i, id := range exerciseIDs {
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT exercise_id, equipment
		FROM exercise_equipment
		WHERE exercise_id IN (`+placeholders(len(exerciseIDs))+`)
		ORDER BY exercise_id, equipment
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var exID string
		var eq Equipment
		if err := rows.Scan(&exID, &eq); err != nil {
			return nil, err
		}
		out[exID] = append(out[exID], eq)
	}
	return out, rows.Err()
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

// SyncCatalog reconciles the exercises table with the given slice on every
// startup. catalog.go is the source of truth: rows present in the slice are
// upserted (new entries inserted, existing entries' non-key fields updated
// to match), and the muscle-group / equipment join tables are fully rebuilt
// per exercise so list changes propagate.
//
// Rows in the DB whose IDs are not in the slice are intentionally left
// alone — soft-delete via DeletedAt is the documented removal path, and we
// don't want a typo or accidental slice mutation to wipe production rows.
// Changing an existing slug is "rare case" territory that should be handled
// out of band, not by this sync.
//
// `updated_at` only bumps when the upsert actually changes something
// (name or description differs), so cosmetic restarts don't churn
// timestamps.
func (r *SQLiteRepository) SyncCatalog(ctx context.Context, exercises []Exercise) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	for _, ex := range exercises {
		// Upsert the exercise row. excluded.* refers to the values we
		// tried to insert; the WHERE clause on the conflict path makes
		// the UPDATE a no-op when the row already matches catalog.go.
		// COALESCE handles the nullable description column — comparing
		// NULL to '' would otherwise be NULL (i.e. "unknown"), causing
		// updated_at to bump on every boot for description-less rows.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO exercises (id, name, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				description = excluded.description,
				updated_at = excluded.updated_at
			WHERE exercises.name != excluded.name
			   OR COALESCE(exercises.description, '') != COALESCE(excluded.description, '')
		`, ex.ID, ex.Name, ex.Description, now, now); err != nil {
			return err
		}

		// Rebuild the muscle-group join table for this exercise. Cheaper
		// and simpler than diffing — at catalog scale it's irrelevant,
		// and it lets adds/removes from the slice flow through cleanly.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM exercise_muscle_groups WHERE exercise_id = ?`, ex.ID); err != nil {
			return err
		}
		for _, mg := range ex.MuscleGroups {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO exercise_muscle_groups (exercise_id, muscle_group) VALUES (?, ?)`,
				ex.ID, mg); err != nil {
				return err
			}
		}

		// Same rebuild pattern for equipment.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM exercise_equipment WHERE exercise_id = ?`, ex.ID); err != nil {
			return err
		}
		for _, eq := range ex.Equipment {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO exercise_equipment (exercise_id, equipment) VALUES (?, ?)`,
				ex.ID, eq); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}
