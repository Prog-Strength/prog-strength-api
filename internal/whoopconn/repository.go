package whoopconn

import (
	"context"
	"time"
)

// Repository persists per-user Whoop connections. Implementations are
// in-memory (dev/test) or SQLite (prod). One row per user, keyed by user_id,
// with whoop_user_id unique for inbound-webhook routing. Token material is
// stored as opaque encrypted blobs — this layer neither encrypts nor decrypts.
type Repository interface {
	// Upsert inserts or replaces the connection: status=connected, connected_at
	// set on first insert (preserved on update), updated_at bumped, whoop_user_id
	// + tokens + scopes written. Reconnect from revoked/error flips status back
	// to connected.
	Upsert(ctx context.Context, userID string, whoopUserID int64, tokens TokenBundle, scopes string, now time.Time) error

	// Get returns metadata (no token). ErrNotFound when absent.
	Get(ctx context.Context, userID string) (*Connection, error)

	// GetByWhoopUserID routes inbound webhooks (which identify users by Whoop ID).
	// ErrNotFound when absent.
	GetByWhoopUserID(ctx context.Context, whoopUserID int64) (*Connection, error)

	// GetTokens returns the stored encrypted token bundle. ErrNotFound when absent.
	GetTokens(ctx context.Context, userID string) (*TokenBundle, error)

	// UpdateTokens persists a rotated access+refresh pair + new expiry (refresh flow).
	// ErrNotFound when absent.
	UpdateTokens(ctx context.Context, userID string, tokens TokenBundle, now time.Time) error

	// SetStatus updates status + updated_at. ErrNotFound when absent.
	SetStatus(ctx context.Context, userID string, status Status, now time.Time) error

	// Revoke marks status=revoked and wipes token columns (disconnect). ErrNotFound when absent.
	Revoke(ctx context.Context, userID string, now time.Time) error

	// Exists reports whether a connection row exists for the user.
	Exists(ctx context.Context, userID string) (bool, error)
}
