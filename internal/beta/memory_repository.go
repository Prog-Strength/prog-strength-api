package beta

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Compile-time check that *MemoryRepository satisfies Repository (and so
// Checker).
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is an in-memory implementation of Repository, safe for
// concurrent use. Data is lost when the process exits — intended for
// development and testing.
type MemoryRepository struct {
	mu     sync.RWMutex
	emails map[string]AllowedEmail // keyed by normalized email
	now    func() time.Time
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		emails: make(map[string]AllowedEmail),
		now:    time.Now,
	}
}

func (r *MemoryRepository) IsAllowed(ctx context.Context, email string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Empty allowlist disables the gate — everyone allowed.
	if len(r.emails) == 0 {
		return true, nil
	}
	_, ok := r.emails[normalizeEmail(email)]
	return ok, nil
}

func (r *MemoryRepository) Add(ctx context.Context, email, addedBy, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	normalized := normalizeEmail(email)
	// Idempotent: keep the original row if already present.
	if _, ok := r.emails[normalized]; ok {
		return nil
	}
	r.emails[normalized] = AllowedEmail{
		Email:   normalized,
		AddedAt: r.now().UTC(),
		AddedBy: nullablePtr(addedBy),
		Note:    nullablePtr(note),
	}
	return nil
}

func (r *MemoryRepository) Remove(ctx context.Context, email string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	normalized := normalizeEmail(email)
	if _, ok := r.emails[normalized]; !ok {
		return false, nil
	}
	delete(r.emails, normalized)
	return true, nil
}

func (r *MemoryRepository) List(ctx context.Context) ([]AllowedEmail, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]AllowedEmail, 0, len(r.emails))
	for _, e := range r.emails {
		out = append(out, copyAllowedEmail(e))
	}
	// Order by added_at ascending; email breaks ties so ordering is stable
	// when timestamps collide (the SQLite backend's PRIMARY KEY gives the
	// same effective determinism within a single timestamp).
	sort.Slice(out, func(i, j int) bool {
		if out[i].AddedAt.Equal(out[j].AddedAt) {
			return out[i].Email < out[j].Email
		}
		return out[i].AddedAt.Before(out[j].AddedAt)
	})
	return out, nil
}

// copyAllowedEmail returns a defensive copy so callers can't mutate the
// pointer fields backing our stored row.
func copyAllowedEmail(e AllowedEmail) AllowedEmail {
	out := e
	if e.AddedBy != nil {
		v := *e.AddedBy
		out.AddedBy = &v
	}
	if e.Note != nil {
		v := *e.Note
		out.Note = &v
	}
	return out
}

// nullablePtr maps an empty string to nil so absent added_by/note match the
// SQLite backend's NULL semantics.
func nullablePtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
