package bodyweight

import (
	"context"
	"time"
)

// Repository persists bodyweight entries. Implementations are
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

	// Delete soft-deletes the entry. Corrections are delete + recreate
	// (no PUT endpoint, per the SOW); this keeps the trend chart's
	// audit trail clean since edits never silently rewrite history.
	Delete(ctx context.Context, userID, id string) error

	// UpdateEntry overwrites an existing entry's weight/unit/measured_at,
	// scoped to user_id. Validation runs server-side. Returns ErrNotFound
	// when the entry is missing, soft-deleted, or cross-user. created_at
	// is never touched — only the mutable measurement fields change.
	UpdateEntry(ctx context.Context, e *Entry) error

	// GetBodyweightGoal returns the user's target weight. When the user
	// has never set a goal the read collapses to a zero-valued Goal with
	// nil timestamps — the client interprets that as "never set" and
	// renders the empty-state affordance. We deliberately do NOT return
	// ErrNotFound for a missing row: "not set" is a value, not an error,
	// exactly like nutrition.GetMacroGoals.
	GetBodyweightGoal(ctx context.Context, userID string) (Goal, error)

	// UpsertBodyweightGoal INSERTs the user's first goal row or replaces
	// the existing one atomically. created_at is set on the initial
	// insert and preserved thereafter; updated_at bumps on every call.
	// Returns the saved goal with real (non-nil) timestamps.
	UpsertBodyweightGoal(ctx context.Context, g Goal, now time.Time) (Goal, error)
}
