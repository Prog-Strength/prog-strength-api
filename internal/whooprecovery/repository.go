package whooprecovery

import (
	"context"
	"time"
)

// Repository persists Whoop daily recovery entries. It mirrors the
// steps-shaped daily-upsert metric contract: one row per (user_id, date),
// latest-wins, hard-deleted. All methods scope by user_id at the storage
// layer so callers don't have to remember an ownership WHERE clause.
type Repository interface {
	// Upsert replaces the row for (user_id, date) — latest wins. On insert an
	// ID is generated (via internal/id, same as steps); on conflict the
	// existing id + created_at are preserved and the metrics/cycle_id/sleep_id/
	// updated_at are overwritten. now is used for created_at (insert only) and
	// updated_at (every call); it is normalized to UTC.
	Upsert(ctx context.Context, e Entry, now time.Time) error

	// ListRange returns rows whose date is within [since, until] inclusive
	// (either may be "" meaning unbounded), newest-first (date DESC).
	ListRange(ctx context.Context, userID, since, until string) ([]Entry, error)

	// DeleteBySleepID removes the row whose sleep_id matches for the user. It
	// is not an error when no row matches (idempotent webhook delete).
	DeleteBySleepID(ctx context.Context, userID, sleepID string) error
}
