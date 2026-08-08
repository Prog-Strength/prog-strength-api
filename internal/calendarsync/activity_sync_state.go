package calendarsync

import (
	"context"
	"errors"
	"time"
)

// ErrSyncStateNotFound is returned when no sync row exists for an activity.
// Absence is a meaningful state here, not an error condition — it means "no
// sync has ever been attempted", which is precisely what the reconciler's
// anti-join looks for — so callers routinely errors.Is this and carry on.
var ErrSyncStateNotFound = errors.New("calendarsync: no sync state for activity")

// SyncStatus is the outcome of the most recent Google write for an activity.
type SyncStatus string

const (
	// SyncPending means a write is owed: either the first insert has not
	// landed, or a previously-synced event was deliberately released (the
	// activity was deleted, or the user disconnected).
	SyncPending SyncStatus = "pending"
	// SyncSynced means the event is live at Google and current.
	SyncSynced SyncStatus = "synced"
	// SyncFailed means the last write failed. The row stays resyncable and
	// the reconciler retries it, bounded by Attempts.
	SyncFailed SyncStatus = "failed"
)

// ActivitySyncState is the Google Calendar bookkeeping for one logged
// activity: which event it owns, whether the last write landed, and how many
// times we have tried.
//
// Prog Strength is the source of truth for the session itself; this row only
// records what we believe Google currently holds. It is deliberately allowed
// to be wrong (a user can delete the event in Google without telling us) —
// the client's ErrEventGone handling is what reconciles the two.
type ActivitySyncState struct {
	ActivityID string
	UserID     string
	// GoogleEventID is the event this activity owns, or nil when none is
	// live. It may be an id ADOPTED from the planned workout this activity
	// completed, in which case the plan row references the same event: the
	// takeover patches the planned event into a completed one rather than
	// leaving a duplicate on the calendar.
	GoogleEventID *string
	Status        SyncStatus
	LastError     *string
	// Attempts counts consecutive failures, reset to 0 on success. It caps
	// the reconciler so an activity Google will never accept cannot be
	// retried on every boot forever.
	Attempts  int
	UpdatedAt time.Time
}

// ActivitySyncRepository persists per-activity calendar sync state. Rows are
// created lazily on the first sync attempt: implementations must upsert, and
// must never require a pre-existing row.
type ActivitySyncRepository interface {
	// Get returns the sync state for one activity, or ErrSyncStateNotFound.
	Get(ctx context.Context, userID, activityID string) (*ActivitySyncState, error)

	// MarkSynced records a successful write: the live event id, status
	// synced, cleared error, and Attempts reset to 0.
	MarkSynced(ctx context.Context, userID, activityID, eventID string, now time.Time) error

	// MarkFailed records a failed write, preserving any event id already
	// held (a failed PATCH does not un-create the event) and incrementing
	// Attempts.
	MarkFailed(ctx context.Context, userID, activityID string, eventID *string, cause string, now time.Time) error

	// Release clears the stored event id and returns the row to pending,
	// without deleting it. Used when the event is gone at Google, or when
	// ownership is handed back — never as part of an error path, which is
	// MarkFailed's job.
	Release(ctx context.Context, userID, activityID string, now time.Time) error

	// PendingSince returns activities belonging to connected users that
	// still owe a Google write: created after the user connected, never
	// successfully synced, and not past maxAttempts. Ordered oldest-first
	// and capped at limit, so a large backlog drains deterministically
	// across boots instead of starving its own tail.
	PendingSince(ctx context.Context, maxAttempts, limit int) ([]PendingSync, error)
}

// PendingSync is one activity the reconciler owes a write for.
type PendingSync struct {
	ActivityID string
	UserID     string
}
