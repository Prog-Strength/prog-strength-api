package whoopsleep

import (
	"context"
	"time"
)

// Repository persists WHOOP sleep records. Unlike whooprecovery's daily
// upsert, the unit of storage is one WHOOP sleep record — see the package doc
// for why. All methods scope by user_id at the storage layer so callers don't
// have to remember an ownership WHERE clause.
type Repository interface {
	// Upsert replaces the row for (user_id, whoop_sleep_id) — latest wins,
	// which is what makes webhook redelivery and out-of-order arrival safe. On
	// insert an ID is generated (via internal/id); on conflict the existing id
	// and created_at are preserved and every other column is overwritten. now
	// is used for created_at (insert only) and updated_at (every call); it is
	// normalized to UTC.
	Upsert(ctx context.Context, e Entry, now time.Time) error

	// ListRange returns rows whose date is within [since, until] inclusive
	// (either may be "" meaning unbounded), newest-first. The order is
	// date DESC, ended_at DESC, whoop_sleep_id DESC: several records can share a
	// date and even an ended_at, and whoop_sleep_id is unique per user, so only
	// all three together are a total order. Callers that tie-break their own
	// selection depend on that determinism.
	ListRange(ctx context.Context, userID, since, until string) ([]Entry, error)

	// DeleteByWhoopSleepID removes the user's row with the matching WHOOP
	// sleep UUID. It is not an error when no row matches (idempotent webhook
	// delete), and it never touches another user's row with the same UUID.
	DeleteByWhoopSleepID(ctx context.Context, userID, whoopSleepID string) error
}
