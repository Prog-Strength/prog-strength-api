package user

import (
	"context"
	"strings"
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
	mu    sync.RWMutex
	users map[string]*User // keyed by ID
	now   func() time.Time // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		users: make(map[string]*User),
		now:   time.Now,
	}
}

func (r *MemoryRepository) Create(ctx context.Context, u *User) error {
	// Default the calendar prefs before validation so memory and sqlite repos
	// behave identically for a newly-built user without them set.
	if u.Timezone == "" {
		u.Timezone = "UTC"
	}
	if u.CalendarDefaultDetail == "" {
		u.CalendarDefaultDetail = "time_block"
	}

	if err := u.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Normalize email for uniqueness check.
	normalizedEmail := normalizeEmail(u.Email)

	// Check if a non-deleted user with this email already exists.
	for _, existing := range r.users {
		if existing.DeletedAt == nil && normalizeEmail(existing.Email) == normalizedEmail {
			return ErrEmailExists
		}
	}

	now := r.now().UTC()
	u.ID = id.New()
	u.Email = normalizedEmail
	u.CreatedAt = now
	u.UpdatedAt = now

	// Store a copy so external mutation doesn't affect our state.
	stored := *u
	r.users[u.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetByID(ctx context.Context, id string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	u, ok := r.users[id]
	if !ok || u.DeletedAt != nil {
		return nil, ErrNotFound
	}
	out := *u
	return &out, nil
}

func (r *MemoryRepository) GetByEmail(ctx context.Context, email string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	normalizedEmail := normalizeEmail(email)
	for _, u := range r.users {
		if u.DeletedAt == nil && normalizeEmail(u.Email) == normalizedEmail {
			out := *u
			return &out, nil
		}
	}
	return nil, ErrNotFound
}

func (r *MemoryRepository) Update(ctx context.Context, u *User) error {
	if err := u.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.users[u.ID]
	if !ok || existing.DeletedAt != nil {
		return ErrNotFound
	}

	// Email is immutable through Update; preserve it from existing record.
	u.Email = existing.Email
	u.CreatedAt = existing.CreatedAt
	u.UpdatedAt = r.now().UTC()
	stored := *u
	r.users[u.ID] = &stored
	return nil
}

func (r *MemoryRepository) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	u, ok := r.users[id]
	if !ok || u.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.now().UTC()
	u.DeletedAt = &now
	u.UpdatedAt = now
	return nil
}

// normalizeEmail lowercases and trims an email address.
// OAuth providers normalize differently, but this is sufficient for
// single-provider (Google) use.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
