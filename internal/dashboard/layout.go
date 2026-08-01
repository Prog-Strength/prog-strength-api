package dashboard

import (
	"context"
	"errors"
)

// ErrLayoutNotFound is returned by Repository.Get when the user has no stored
// layout row (they have never customized). Callers resolve the default layout.
var ErrLayoutNotFound = errors.New("dashboard: layout not found")

// Layout is a user's persisted dashboard layout: the ordered set of enabled
// tile ids. On read, unknown/retired ids are filtered out (the catalog is the
// source of truth), so TileIDs contains only currently-valid ids.
type Layout struct {
	TileIDs []TileID
}

// Repository persists one dashboard layout per user, keyed by user_id.
type Repository interface {
	// Get returns the stored layout, filtered to currently-valid tile ids.
	// ErrLayoutNotFound when the user has no row.
	Get(ctx context.Context, userID string) (Layout, error)
	// Upsert writes the ordered tile ids for the user (insert or replace).
	Upsert(ctx context.Context, userID string, tileIDs []TileID) error
}
