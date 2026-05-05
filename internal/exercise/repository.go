package exercise

import "context"

// Repository provides read access to the exercise catalog.
// Writes are intentionally absent: the catalog is curated and
// shipped with the application. Admin-managed writes, if added
// later, will live on a separate interface.
type Repository interface {
	// GetByID returns an exercise by its ID. Returns ErrNotFound if
	// no exercise exists with that ID, or if it has been soft-deleted.
	GetByID(ctx context.Context, id string) (*Exercise, error)

	// List returns all exercises matching the given filters,
	// sorted alphabetically by name. Soft-deleted exercises are excluded.
	List(ctx context.Context, opts ListOptions) ([]Exercise, error)
}

// ListOptions controls filtering for List. All fields are optional;
// zero values mean "no filter on this dimension."
type ListOptions struct {
	// MuscleGroup filters to exercises that target this muscle group.
	// Matches if the exercise lists it among its MuscleGroups.
	MuscleGroup MuscleGroup

	// Equipment filters to exercises that require this equipment.
	// Matches if the exercise lists it among its Equipment.
	Equipment Equipment
}
