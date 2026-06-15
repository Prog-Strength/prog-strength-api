package steps

import (
	"context"
	"time"
)

// Repository persists daily step entries. Implementations are in-memory
// (dev/test default) or SQLite (prod). All methods enforce ownership at
// the storage layer so handlers don't have to remember a user_id WHERE
// clause; cross-user dates return ErrNotFound.
type Repository interface {
	// UpsertEntry inserts a new (user_id, date) row or replaces the step
	// count on the existing one in a single statement. The implementation
	// sets ID and CreatedAt on first insert and preserves CreatedAt on
	// conflict; UpdatedAt bumps on every call. Validation runs server-side;
	// Validate-failing input is rejected without a DB round trip. Returns
	// the stored entry with real timestamps.
	UpsertEntry(ctx context.Context, e *Entry) (Entry, error)

	// List returns the user's entries, most recent date first, in one of
	// two modes:
	//
	//   - Keyset (limit > 0): up to limit rows with date < before when
	//     before is non-nil, newest first. nextBefore is the date of the
	//     last row when a full page was returned (more may exist), else "".
	//     since/until are ignored in this mode.
	//   - Range (limit == 0): every row with since <= date <= until, both
	//     bounds inclusive and either may be nil for an open bound.
	//     nextBefore is always "".
	List(ctx context.Context, userID string, since, until *string, limit int, before *string) (entries []Entry, nextBefore string, err error)

	// Delete hard-deletes the (user_id, date) row. Returns ErrNotFound
	// when no entry exists for that user and day.
	Delete(ctx context.Context, userID, date string) error

	// GetGoal returns the user's daily step goal. When the user has never
	// set a goal the read collapses to a zero-valued Goal with nil
	// timestamps — the client interprets that as "never set" and renders
	// the empty-state affordance. We deliberately do NOT return ErrNotFound
	// for a missing row: "not set" is a value, not an error, exactly like
	// the bodyweight goal.
	GetGoal(ctx context.Context, userID string) (Goal, error)

	// UpsertGoal INSERTs the user's first goal row or replaces the existing
	// one atomically. created_at is set on the initial insert and preserved
	// thereafter; updated_at bumps on every call. Returns the saved goal
	// with real (non-nil) timestamps.
	UpsertGoal(ctx context.Context, g Goal, now time.Time) (Goal, error)
}
