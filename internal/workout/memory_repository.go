package workout

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is an in-memory implementation of Repository.
// It's safe for concurrent use. Data is lost when the process exits —
// intended for development, testing, and early prototyping.
type MemoryRepository struct {
	mu       sync.RWMutex
	workouts map[string]*Workout
	now      func() time.Time // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		workouts: make(map[string]*Workout),
		now:      time.Now,
	}
}

func (r *MemoryRepository) Create(ctx context.Context, w *Workout) error {
	if err := w.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now().UTC()
	w.ID = id.New()
	w.CreatedAt = now
	w.UpdatedAt = now

	// Store a copy so external mutation doesn't affect our state.
	stored := *w
	r.workouts[w.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetByID(ctx context.Context, id string) (*Workout, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	w, ok := r.workouts[id]
	if !ok || w.DeletedAt != nil {
		return nil, ErrNotFound
	}
	out := *w
	return &out, nil
}

func (r *MemoryRepository) ListByUser(ctx context.Context, userID string, opts ListOptions) ([]Workout, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []Workout
	for _, w := range r.workouts {
		if w.UserID != userID || w.DeletedAt != nil {
			continue
		}
		if opts.Since != nil && w.PerformedAt.Before(*opts.Since) {
			continue
		}
		if opts.Until != nil && w.PerformedAt.After(*opts.Until) {
			continue
		}
		results = append(results, *w)
	}

	// Most recent first.
	sort.Slice(results, func(i, j int) bool {
		return results[i].PerformedAt.After(results[j].PerformedAt)
	})

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if opts.Offset >= len(results) {
		return []Workout{}, nil
	}
	end := opts.Offset + limit
	if end > len(results) {
		end = len(results)
	}
	return results[opts.Offset:end], nil
}

func (r *MemoryRepository) Update(ctx context.Context, w *Workout) error {
	if err := w.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.workouts[w.ID]
	if !ok || existing.DeletedAt != nil {
		return ErrNotFound
	}

	w.CreatedAt = existing.CreatedAt
	w.UpdatedAt = r.now().UTC()
	stored := *w
	r.workouts[w.ID] = &stored
	return nil
}

func (r *MemoryRepository) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workouts[id]
	if !ok || w.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.now().UTC()
	w.DeletedAt = &now
	w.UpdatedAt = now
	return nil
}
