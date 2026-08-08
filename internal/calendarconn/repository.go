package calendarconn

import (
	"context"
	"time"
)

// Repository persists per-user Google Calendar connections. Implementations
// are in-memory (dev/test) or SQLite (prod). One row per user, keyed by
// user_id. Token material is stored as opaque encrypted blobs — this layer
// neither encrypts nor decrypts.
type Repository interface {
	// Upsert inserts or replaces the user's connection (encrypted token +
	// metadata), setting status=connected and connected_at on first insert
	// (preserved on update) and bumping updated_at. Re-connecting a revoked
	// user flips status back to connected.
	Upsert(ctx context.Context, userID string, refreshTokenEnc, nonce []byte, calendarID, scopes string, now time.Time) error

	// Get returns connection metadata (no token). ErrNotFound when absent.
	Get(ctx context.Context, userID string) (*Connection, error)

	// GetRefreshToken returns the stored encrypted token + nonce, exactly as
	// written by Upsert. ErrNotFound when absent.
	GetRefreshToken(ctx context.Context, userID string) (enc, nonce []byte, err error)

	// SetStatus updates status (e.g. revoked) and updated_at. ErrNotFound
	// when absent.
	SetStatus(ctx context.Context, userID string, status Status, now time.Time) error

	// Delete removes the row. ErrNotFound when absent.
	Delete(ctx context.Context, userID string) error

	// Exists reports whether a connection row exists for the user.
	Exists(ctx context.Context, userID string) (bool, error)

	// MarkSynced records that a Google write succeeded for this user,
	// advancing last_successful_sync_at. Best-effort by contract: callers
	// ignore its error, because failing a user's request over a metrics
	// stamp would be worse than a stale gauge. ErrNotFound when absent.
	MarkSynced(ctx context.Context, userID string, now time.Time) error

	// List returns every connection (no token material), for the metrics
	// exporter. Unfiltered and unpaginated, matching whoopconn.List: the
	// row count is bounded by opted-in users, and the exporter needs the
	// full set to publish per-status counts.
	List(ctx context.Context) ([]Connection, error)
}
