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
	goals   map[string]*Goal  // user_id → goal (per-user singleton)
	nowFunc func() time.Time  // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		entries: make(map[string]*Entry),
		goals:   make(map[string]*Goal),
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

func (r *MemoryRepository) UpdateEntry(ctx context.Context, e *Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.entries[e.ID]
	if !ok || existing.UserID != e.UserID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	// Only the mutable measurement fields change; ID, UserID, CreatedAt,
	// and DeletedAt are preserved.
	existing.Weight = e.Weight
	existing.Unit = e.Unit
	existing.MeasuredAt = e.MeasuredAt
	return nil
}

// GetBodyweightGoal returns a defensive copy of the user's goal, or a
// zero-valued Goal (the "never set" state) when absent. See the
// Repository interface comment.
func (r *MemoryRepository) GetBodyweightGoal(ctx context.Context, userID string) (Goal, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	g, ok := r.goals[userID]
	if !ok {
		return Goal{UserID: userID}, nil
	}
	// Defensive copy, including fresh timestamp pointers, so callers
	// never hold a pointer into internal state.
	cp := *g
	if g.CreatedAt != nil {
		t := *g.CreatedAt
		cp.CreatedAt = &t
	}
	if g.UpdatedAt != nil {
		t := *g.UpdatedAt
		cp.UpdatedAt = &t
	}
	return cp, nil
}

func (r *MemoryRepository) UpsertBodyweightGoal(ctx context.Context, g Goal, now time.Time) (Goal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	created := now
	if existing, ok := r.goals[g.UserID]; ok && existing.CreatedAt != nil {
		created = *existing.CreatedAt
	}
	updated := now

	// Store a copy with its own timestamp pointers so callers never hold
	// a pointer into internal state.
	stored := g
	sc, su := created, updated
	stored.CreatedAt, stored.UpdatedAt = &sc, &su
	r.goals[g.UserID] = &stored

	out := g
	oc, ou := created, updated
	out.CreatedAt, out.UpdatedAt = &oc, &ou
	return out, nil
}
