package nutritionlookup

import (
	"context"
	"sync"
	"time"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation, used when
// DATABASE_URL is empty. Same semantics as SQLite — Get bumps
// last_used_at, Put sweeps — the cache is simply non-durable.
type MemoryRepository struct {
	mu      sync.Mutex
	rows    map[string]CacheRow // queryNormalized → row
	nowFunc func() time.Time    // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		rows:    make(map[string]CacheRow),
		nowFunc: time.Now,
	}
}

func (r *MemoryRepository) Get(ctx context.Context, queryNormalized string) (*CacheRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	row, ok := r.rows[queryNormalized]
	if !ok {
		return nil, nil
	}
	row.LastUsedAt = r.nowFunc().UTC()
	r.rows[queryNormalized] = row
	cp := row
	return &cp, nil
}

func (r *MemoryRepository) Put(ctx context.Context, row CacheRow) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rows[row.QueryNormalized] = row
	cutoff := r.nowFunc().UTC().Add(-evictionAge)
	for key, existing := range r.rows {
		if existing.LastUsedAt.Before(cutoff) {
			delete(r.rows, key)
		}
	}
	return nil
}
