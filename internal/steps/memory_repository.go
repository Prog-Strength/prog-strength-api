package steps

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation. Entries are
// keyed by userID+"|"+date so the (user, date) upsert collapses to a map
// write; goals are keyed by userID (per-user singleton). State is guarded
// by a RW mutex — same pattern as the bodyweight package.
type MemoryRepository struct {
	mu      sync.RWMutex
	entries map[string]*Entry // userID|date → entry
	goals   map[string]*Goal  // userID → goal (per-user singleton)
	nowFunc func() time.Time  // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		entries: make(map[string]*Entry),
		goals:   make(map[string]*Goal),
		nowFunc: time.Now,
	}
}

// entryKey is the composite map key enforcing one row per (user, date) —
// the in-memory analog of the UNIQUE (user_id, date) constraint.
func entryKey(userID, date string) string {
	return userID + "|" + date
}

func (r *MemoryRepository) UpsertEntry(ctx context.Context, e *Entry) (Entry, error) {
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc().UTC()
	key := entryKey(e.UserID, e.Date)

	created := now
	entryID := id.New()
	if existing, ok := r.entries[key]; ok {
		// Conflict: preserve the original id + created_at, replace the
		// count, bump updated_at — same semantics as the SQL ON CONFLICT.
		created = existing.CreatedAt
		entryID = existing.ID
	}

	stored := Entry{
		ID:        entryID,
		UserID:    e.UserID,
		Date:      e.Date,
		Steps:     e.Steps,
		CreatedAt: created,
		UpdatedAt: now,
	}
	cp := stored
	r.entries[key] = &cp
	return stored, nil
}

func (r *MemoryRepository) List(ctx context.Context, userID string, since, until *string, limit int, before *string) ([]Entry, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keyset := limit > 0

	var out []Entry
	for _, e := range r.entries {
		if e.UserID != userID {
			continue
		}
		if keyset {
			if before != nil && e.Date >= *before {
				continue
			}
		} else {
			// Lexicographic comparison is calendar order for YYYY-MM-DD.
			// Both bounds inclusive.
			if since != nil && e.Date < *since {
				continue
			}
			if until != nil && e.Date > *until {
				continue
			}
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Date > out[j].Date
	})

	if keyset && len(out) > limit {
		out = out[:limit]
	}

	nextBefore := ""
	if keyset && len(out) == limit {
		nextBefore = out[len(out)-1].Date
	}
	return out, nextBefore, nil
}

func (r *MemoryRepository) Delete(ctx context.Context, userID, date string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := entryKey(userID, date)
	if _, ok := r.entries[key]; !ok {
		return ErrNotFound
	}
	delete(r.entries, key)
	return nil
}

// GetGoal returns a defensive copy of the user's goal, or a zero-valued
// Goal (the "never set" state) when absent. See the Repository interface
// comment.
func (r *MemoryRepository) GetGoal(ctx context.Context, userID string) (Goal, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	g, ok := r.goals[userID]
	if !ok {
		return Goal{UserID: userID}, nil
	}
	// Defensive copy, including fresh timestamp pointers, so callers never
	// hold a pointer into internal state.
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

func (r *MemoryRepository) UpsertGoal(ctx context.Context, g Goal, now time.Time) (Goal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	created := now
	if existing, ok := r.goals[g.UserID]; ok && existing.CreatedAt != nil {
		created = *existing.CreatedAt
	}
	updated := now

	// Store a copy with its own timestamp pointers so callers never hold a
	// pointer into internal state.
	stored := g
	sc, su := created, updated
	stored.CreatedAt, stored.UpdatedAt = &sc, &su
	r.goals[g.UserID] = &stored

	out := g
	oc, ou := created, updated
	out.CreatedAt, out.UpdatedAt = &oc, &ou
	return out, nil
}
