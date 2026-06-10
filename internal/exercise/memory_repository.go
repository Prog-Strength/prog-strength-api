package exercise

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is an in-memory implementation of Repository
// backed by a fixed catalog. It's safe for concurrent reads.
type MemoryRepository struct {
	mu        sync.RWMutex
	exercises map[string]Exercise
}

// NewMemoryRepository builds a repository seeded from the given catalog.
// Pass exercise.Catalog for the canonical seed; tests can pass a smaller
// fixture.
func NewMemoryRepository(catalog []Exercise) *MemoryRepository {
	r := &MemoryRepository{
		exercises: make(map[string]Exercise, len(catalog)),
	}
	for _, ex := range catalog {
		r.exercises[ex.ID] = ex
	}
	return r
}

func (r *MemoryRepository) GetByID(ctx context.Context, id string) (*Exercise, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ex, ok := r.exercises[id]
	if !ok || ex.DeletedAt != nil {
		return nil, ErrNotFound
	}
	out := ex
	return &out, nil
}

func (r *MemoryRepository) List(ctx context.Context, opts ListOptions) ([]Exercise, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	results := make([]Exercise, 0, len(r.exercises))
	for _, ex := range r.exercises {
		if ex.DeletedAt != nil {
			continue
		}
		if len(opts.MuscleGroups) > 0 {
			if !containsAnyMuscleGroup(ex.MuscleGroups, opts.MuscleGroups) {
				continue
			}
		} else if opts.MuscleGroup != "" && !containsMuscleGroup(ex.MuscleGroups, opts.MuscleGroup) {
			continue
		}
		if opts.Equipment != "" && !containsEquipment(ex.Equipment, opts.Equipment) {
			continue
		}
		results = append(results, ex)
	}

	sort.Slice(results, func(i, j int) bool {
		return strings.ToLower(results[i].Name) < strings.ToLower(results[j].Name)
	})

	return results, nil
}

func containsMuscleGroup(haystack []MuscleGroup, needle MuscleGroup) bool {
	for _, m := range haystack {
		if m == needle {
			return true
		}
	}
	return false
}

// containsAnyMuscleGroup reports whether haystack shares at least one
// muscle group with needles (OR semantics for the movement-pattern
// rollup).
func containsAnyMuscleGroup(haystack, needles []MuscleGroup) bool {
	for _, n := range needles {
		if containsMuscleGroup(haystack, n) {
			return true
		}
	}
	return false
}

func containsEquipment(haystack []Equipment, needle Equipment) bool {
	for _, e := range haystack {
		if e == needle {
			return true
		}
	}
	return false
}
