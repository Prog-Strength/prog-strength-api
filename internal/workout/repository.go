package workout

import (
	"context"
	"time"
)

// Repository persists workouts. Implementations may be backed by
// in-memory storage, SQLite, Postgres, etc. The interface is defined
// in domain language — callers should not need to know how workouts
// are stored.
type Repository interface {
	// Create persists a new workout. The implementation is responsible
	// for setting ID, CreatedAt, and UpdatedAt; callers should leave
	// these zero-valued.
	Create(ctx context.Context, w *Workout) error

	// GetByID returns a workout by its ID. Returns ErrNotFound if no
	// workout exists with that ID, or if it has been soft-deleted.
	GetByID(ctx context.Context, id string) (*Workout, error)

	// ListByUser returns workouts for a user, most recent first.
	// Soft-deleted workouts are excluded.
	ListByUser(ctx context.Context, userID string, opts ListOptions) ([]Workout, error)

	// Update replaces an existing workout. Returns ErrNotFound if the
	// workout doesn't exist or is soft-deleted.
	Update(ctx context.Context, w *Workout) error

	// Delete soft-deletes a workout by setting DeletedAt.
	// Returns ErrNotFound if the workout doesn't exist or is already deleted.
	Delete(ctx context.Context, id string) error
}

// ListOptions controls pagination and filtering for list operations.
type ListOptions struct {
	Limit  int        // 0 means use a sensible default (e.g., 50)
	Offset int        // for pagination
	Since  *time.Time // only workouts performed at or after this time
	Until  *time.Time // only workouts performed at or before this time
}
