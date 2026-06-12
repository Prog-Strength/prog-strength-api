package nutritionlookup

import (
	"context"
	"time"
)

// CacheRow is one nutrition_lookup_cache row: per-serving candidates
// for a normalized query, plus the two timestamps driving the
// freshness (FetchedAt) and eviction (LastUsedAt) policies.
type CacheRow struct {
	QueryNormalized string
	CandidatesJSON  string
	FetchedAt       time.Time
	LastUsedAt      time.Time
}

// evictionAge is how long an unused cache row survives before the
// opportunistic sweep on Put deletes it. Code-pinned, not env — same
// philosophy as auth.JWTLifetime and freshnessTTL: changing retention
// semantics should be a reviewable code change.
const evictionAge = 90 * 24 * time.Hour

// Repository persists the global (not per-user) nutrition lookup
// cache. Implementations are in-memory (dev/test default) or SQLite
// (prod, durable across deploys).
type Repository interface {
	// Get returns the row for the normalized query, bumping its
	// last_used_at so hot foods never age out. Returns (nil, nil) on a
	// miss — absence is an expected state, not an error.
	Get(ctx context.Context, queryNormalized string) (*CacheRow, error)

	// Put upserts the row, then piggybacks the eviction sweep: rows
	// whose last_used_at is older than evictionAge are deleted. No
	// background job; table growth is bounded by write traffic.
	Put(ctx context.Context, row CacheRow) error
}
