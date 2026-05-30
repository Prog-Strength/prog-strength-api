package bodyweight

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation. Holds
// state in a single map protected by a RW mutex — same pattern as
// the nutrition and workout packages.
type MemoryRepository struct {
	mu      sync.RWMutex
	entries map[string]*Entry // id → entry
	nowFunc func() time.Time  // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		entries: make(map[string]*Entry),
		nowFunc: time.Now,
	}
}

func (r *MemoryRepository) Create(ctx context.Context, e *Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc().UTC()
	e.ID = id.New()
	e.CreatedAt = now
	e.DeletedAt = nil

	stored := *e
	r.entries[e.ID] = &stored
	return nil
}

func (r *MemoryRepository) Get(ctx context.Context, userID, entryID string) (*Entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	e, ok := r.entries[entryID]
	if !ok || e.UserID != userID || e.DeletedAt != nil {
		return nil, ErrNotFound
	}
	cp := *e
	return &cp, nil
}

func (r *MemoryRepository) List(ctx context.Context, userID string, since, until *time.Time) ([]Entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Entry
	for _, e := range r.entries {
		if e.UserID != userID || e.DeletedAt != nil {
			continue
		}
		if since != nil && e.MeasuredAt.Before(*since) {
			continue
		}
		if until != nil && !e.MeasuredAt.Before(*until) {
			continue
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].MeasuredAt.After(out[j].MeasuredAt)
	})
	return out, nil
}

func (r *MemoryRepository) Delete(ctx context.Context, userID, entryID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.entries[entryID]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	existing.DeletedAt = &now
	return nil
}
