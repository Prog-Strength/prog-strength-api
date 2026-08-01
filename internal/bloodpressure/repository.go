package bloodpressure

import (
	"context"
	"time"
)

// Repository persists blood-pressure entries. Implementations are
// in-memory (dev/test default) or SQLite (prod). All methods enforce
// ownership at the storage layer so handlers don't have to remember a
// user_id WHERE clause; cross-user IDs return ErrNotFound.
type Repository interface {
	// Create persists a new entry. The implementation is responsible
	// for setting ID and CreatedAt; callers should leave those zero.
	// Validation runs server-side; Validate-failing input is rejected
	// without a DB round trip.
	Create(ctx context.Context, e *Entry) error

	// Get returns the entry by ID, scoped to user_id. Returns
	// ErrNotFound when missing, soft-deleted, or cross-user.
	Get(ctx context.Context, userID, id string) (*Entry, error)

	// List returns the user's non-deleted entries, most recent
	// MeasuredAt first. since/until bound MeasuredAt (since inclusive,
	// until exclusive). Either may be nil for an open bound.
	List(ctx context.Context, userID string, since, until *time.Time) ([]Entry, error)

	// UpdateEntry overwrites an existing entry's systolic/diastolic/pulse/
	// measured_at, scoped to user_id. Validation runs server-side. Returns
	// ErrNotFound when the entry is missing, soft-deleted, or cross-user.
	// created_at is never touched — only the mutable measurement fields
	// change. Unlike bodyweight this domain exposes a PUT endpoint, so
	// edits mutate in place rather than delete + recreate.
	UpdateEntry(ctx context.Context, e *Entry) error

	// Delete soft-deletes the entry, scoped to user_id. Returns ErrNotFound
	// when the entry is missing, already deleted, or cross-user.
	Delete(ctx context.Context, userID, id string) error
}
