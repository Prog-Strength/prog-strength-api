package plannedworkout

import (
	"context"
	"time"
)

// Repository persists planned workouts and their agendas. Implementations
// are in-memory (dev/test default) or SQLite (prod). All methods enforce
// ownership at the storage layer so handlers don't have to remember a
// user_id WHERE clause; cross-user IDs return ErrNotFound.
type Repository interface {
	// Create persists a new plan and its full agenda atomically. The
	// implementation sets ID, CreatedAt, and UpdatedAt; callers should
	// leave those zero. Status defaults to "planned" when empty. Validation
	// runs server-side first, so Validate-failing input is rejected without
	// a DB round trip.
	Create(ctx context.Context, pw *PlannedWorkout) error

	// Get returns the plan by ID, scoped to user_id, with its agenda
	// hydrated and ordered by order_index. Returns ErrNotFound when missing,
	// soft-deleted, or cross-user.
	Get(ctx context.Context, userID, id string) (*PlannedWorkout, error)

	// List returns the user's non-deleted plans whose ScheduledStartUTC
	// falls in [since, until). Either bound may be nil for an open end.
	// Results are ascending by ScheduledStartUTC with agendas hydrated.
	List(ctx context.Context, userID string, since, until *time.Time) ([]PlannedWorkout, error)

	// Update overwrites the plan's mutable fields and REPLACES its agenda
	// atomically (old exercises/sets are dropped, the supplied ones are
	// reinserted with fresh ids). ID, UserID, and CreatedAt are preserved;
	// UpdatedAt bumps. Validation runs server-side first. Returns
	// ErrNotFound when missing, soft-deleted, or cross-user.
	Update(ctx context.Context, pw *PlannedWorkout) error

	// Delete soft-deletes the plan. Returns ErrNotFound when missing,
	// already deleted, or cross-user.
	Delete(ctx context.Context, userID, id string) error

	// SetStatus transitions the plan's lifecycle status. Returns
	// ErrNotFound when missing, soft-deleted, or cross-user.
	SetStatus(ctx context.Context, userID, id string, status Status) error

	// SetCompletion marks the plan completed and links it to the session
	// that fulfilled it (a workout or an activity). Returns ErrNotFound when
	// missing, soft-deleted, or cross-user.
	SetCompletion(ctx context.Context, userID, id, sessionID string, kind SessionKind) error

	// ClearCompletion reverts a completed plan to "planned" and clears its
	// completion link. Inverse of SetCompletion. Returns ErrNotFound when the
	// plan is missing, soft-deleted, or cross-user.
	ClearCompletion(ctx context.Context, userID, id string) error

	// GetByCompletedSession returns the user's non-deleted plan whose completion
	// link points at (sessionID, kind), or ErrNotFound when none does.
	GetByCompletedSession(ctx context.Context, userID, sessionID string, kind SessionKind) (*PlannedWorkout, error)

	// SetGoogleSync records the outcome of a Google Calendar sync attempt:
	// the (possibly nil) event id, the sync status, and the last error
	// message (nil on success). Passing a nil eventID clears it. Returns
	// ErrNotFound when missing, soft-deleted, or cross-user.
	SetGoogleSync(ctx context.Context, userID, id string, eventID *string, status GoogleSyncStatus, lastErr *string) error
}
