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

	// Load muscle groups.
	muscleGroups, err := r.getMuscleGroups(ctx, id)
	if err != nil {
		return nil, err
	}
	ex.MuscleGroups = muscleGroups

	// Load equipment.
	equipment, err := r.getEquipment(ctx, id)
	if err != nil {
		return nil, err
	}
	ex.Equipment = equipment

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

	// Drain the parent rows into a slice before fanning out to
	// getMuscleGroups / getEquipment. Calling those inside rows.Next()
	// holds a connection from the pool while issuing another query —
	// with a handful of concurrent /exercises requests every connection
	// ends up waiting on a nested call that can never acquire a free
	// conn, and the pool deadlocks. Same fix shape used in the workout
	// repo's ListByUser.
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var exercises []Exercise
	for rows.Next() {
		var ex Exercise
		if err := rows.Scan(&ex.ID, &ex.Name, &ex.Description, &ex.CreatedAt, &ex.UpdatedAt, &ex.DeletedAt); err != nil {
			rows.Close()
			return nil, err
		}
		exercises = append(exercises, ex)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range exercises {
		muscleGroups, err := r.getMuscleGroups(ctx, exercises[i].ID)
		if err != nil {
			return nil, err
		}
		exercises[i].MuscleGroups = muscleGroups

		equipment, err := r.getEquipment(ctx, exercises[i].ID)
		if err != nil {
			return nil, err
		}
		exercises[i].Equipment = equipment
	}

	// Sort alphabetically by name (case-insensitive).
	sort.Slice(exercises, func(i, j int) bool {
		return strings.ToLower(exercises[i].Name) < strings.ToLower(exercises[j].Name)
	})

	return exercises, nil
}

// getMuscleGroups fetches all muscle groups for an exercise.
func (r *SQLiteRepository) getMuscleGroups(ctx context.Context, exerciseID string) ([]MuscleGroup, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT muscle_group
		FROM exercise_muscle_groups
		WHERE exercise_id = ?
		ORDER BY muscle_group
	`, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []MuscleGroup
	for rows.Next() {
		var mg MuscleGroup
		if err := rows.Scan(&mg); err != nil {
			return nil, err
		}
		groups = append(groups, mg)
	}

	return groups, rows.Err()
}

// getEquipment fetches all equipment for an exercise.
func (r *SQLiteRepository) getEquipment(ctx context.Context, exerciseID string) ([]Equipment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT equipment
		FROM exercise_equipment
		WHERE exercise_id = ?
		ORDER BY equipment
	`, exerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var equipment []Equipment
	for rows.Next() {
		var eq Equipment
		if err := rows.Scan(&eq); err != nil {
			return nil, err
		}
		equipment = append(equipment, eq)
	}

	return equipment, rows.Err()
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
