package exercise

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
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

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var exercises []Exercise
	for rows.Next() {
		var ex Exercise
		if err := rows.Scan(&ex.ID, &ex.Name, &ex.Description, &ex.CreatedAt, &ex.UpdatedAt, &ex.DeletedAt); err != nil {
			return nil, err
		}

		// Load muscle groups for this exercise.
		muscleGroups, err := r.getMuscleGroups(ctx, ex.ID)
		if err != nil {
			return nil, err
		}
		ex.MuscleGroups = muscleGroups

		// Load equipment for this exercise.
		equipment, err := r.getEquipment(ctx, ex.ID)
		if err != nil {
			return nil, err
		}
		ex.Equipment = equipment

		exercises = append(exercises, ex)
	}

	if err := rows.Err(); err != nil {
		return nil, err
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

// SeedCatalog inserts the given exercises if the exercises table is empty.
// This is called on startup to populate the catalog from exercise.Catalog.
func (r *SQLiteRepository) SeedCatalog(ctx context.Context, exercises []Exercise) error {
	// Check if exercises table is empty.
	var count int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exercises").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		// Already seeded.
		return nil
	}

	// Insert all exercises in a transaction.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, ex := range exercises {
		// Insert exercise.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO exercises (id, name, description, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
		`, ex.ID, ex.Name, ex.Description, ex.CreatedAt, ex.UpdatedAt)
		if err != nil {
			return err
		}

		// Insert muscle groups.
		for _, mg := range ex.MuscleGroups {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO exercise_muscle_groups (exercise_id, muscle_group)
				VALUES (?, ?)
			`, ex.ID, mg)
			if err != nil {
				return err
			}
		}

		// Insert equipment.
		for _, eq := range ex.Equipment {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO exercise_equipment (exercise_id, equipment)
				VALUES (?, ?)
			`, ex.ID, eq)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}
