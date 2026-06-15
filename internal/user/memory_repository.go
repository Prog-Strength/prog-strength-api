package user

import (
	"context"
	"sort"
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

	// Enforce case-insensitive username uniqueness (mirrors the SQLite unique
	// index). A nil username is "unset" and never collides.
	if collidesUsername(r.users, u.Username, "") {
		return ErrUsernameTaken
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

func (r *MemoryRepository) GetByUsername(ctx context.Context, username string) (*User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, u := range r.users {
		if u.DeletedAt == nil && u.Username != nil && *u.Username == username {
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

	// Enforce case-insensitive username uniqueness against every OTHER user
	// (a user keeping their own handle is not a collision).
	if collidesUsername(r.users, u.Username, u.ID) {
		return ErrUsernameTaken
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

// SearchProfiles implements the ranked, keyset-paginated profile search in Go,
// matching the SQLite backend's semantics exactly (driven by a shared contract
// test). It filters + computes each match's best bucket, sorts by
// (bucket, sortkey, id), applies the keyset cursor, and slices to limit+1 to
// derive the next cursor. See the Repository interface doc for the
// follower_count tiebreak deferral.
func (r *MemoryRepository) SearchProfiles(ctx context.Context, query string, limit int, after *SearchCursor) ([]*User, *SearchCursor, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return []*User{}, nil, nil
	}
	if limit < 1 {
		limit = 1
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	type match struct {
		u       *User
		bucket  int
		sortkey string
	}
	var matches []match
	for _, u := range r.users {
		if u.DeletedAt != nil {
			continue
		}
		bucket := -1
		// Username predicates only apply when a username is set (NULL guard).
		if u.Username != nil {
			lu := strings.ToLower(*u.Username)
			switch {
			case lu == q:
				bucket = 0
			case strings.HasPrefix(lu, q):
				bucket = 1
			}
		}
		// Substring display-name match is bucket 2, but only if no better
		// username match already classified this user (take the best/min bucket).
		if bucket == -1 && strings.Contains(strings.ToLower(u.DisplayName), q) {
			bucket = 2
		}
		if bucket == -1 {
			continue
		}
		sortkey := strings.ToLower(u.DisplayName)
		if u.Username != nil {
			sortkey = strings.ToLower(*u.Username)
		}
		out := *u
		matches = append(matches, match{u: &out, bucket: bucket, sortkey: sortkey})
	}

	// Total order: (bucket ASC, sortkey ASC, id ASC).
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].bucket != matches[j].bucket {
			return matches[i].bucket < matches[j].bucket
		}
		if matches[i].sortkey != matches[j].sortkey {
			return matches[i].sortkey < matches[j].sortkey
		}
		return matches[i].u.ID < matches[j].u.ID
	})

	// Apply the keyset cursor: keep only rows strictly greater than `after`.
	start := 0
	if after != nil {
		for start < len(matches) {
			m := matches[start]
			if m.bucket > after.Bucket ||
				(m.bucket == after.Bucket && m.sortkey > after.SortKey) ||
				(m.bucket == after.Bucket && m.sortkey == after.SortKey && m.u.ID > after.ID) {
				break
			}
			start++
		}
	}
	matches = matches[start:]

	var next *SearchCursor
	if len(matches) > limit {
		last := matches[limit-1]
		next = &SearchCursor{Bucket: last.bucket, SortKey: last.sortkey, ID: last.u.ID}
		matches = matches[:limit]
	}

	users := make([]*User, 0, len(matches))
	for _, m := range matches {
		users = append(users, m.u)
	}
	return users, next, nil
}

// collidesUsername reports whether a non-deleted user other than excludeID
// already holds the given (case-insensitively compared) username. A nil
// username never collides — it represents an unset handle, matching SQLite's
// multiple-NULLs-allowed unique index. Stored usernames are already canonical
// (lowercased), but the comparison lowercases defensively.
func collidesUsername(users map[string]*User, username *string, excludeID string) bool {
	if username == nil {
		return false
	}
	want := strings.ToLower(*username)
	for id, existing := range users {
		if id == excludeID || existing.DeletedAt != nil || existing.Username == nil {
			continue
		}
		if strings.ToLower(*existing.Username) == want {
			return true
		}
	}
	return false
}

// normalizeEmail lowercases and trims an email address.
// OAuth providers normalize differently, but this is sufficient for
// single-provider (Google) use.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
