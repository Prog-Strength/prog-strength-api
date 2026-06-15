package user

import "context"

// SearchCursor is the opaque keyset position for profile search. It is the
// (rank bucket, stable sort key, id) tuple of the last row on a page — a total
// order — so the next page asks for rows strictly greater than this position in
// (bucket ASC, SortKey ASC, ID ASC). Bucket is the match-quality rank (0 exact
// username, 1 prefix username, 2 substring display-name); SortKey is
// COALESCE(lower(username), lower(display_name)); ID is the final tiebreaker.
type SearchCursor struct {
	Bucket  int
	SortKey string
	ID      string
}

// Repository persists users. Implementations may be backed by
// in-memory storage, SQLite, Postgres, etc. The interface is defined
// in domain language — callers should not need to know how users
// are stored.
type Repository interface {
	// Create persists a new user. The implementation is responsible
	// for setting ID, CreatedAt, and UpdatedAt; callers should leave
	// these zero-valued. Returns ErrEmailExists if a non-deleted user
	// with the same email already exists.
	Create(ctx context.Context, u *User) error

	// GetByID returns a user by their ID. Returns ErrNotFound if no
	// user exists with that ID, or if they have been soft-deleted.
	GetByID(ctx context.Context, id string) (*User, error)

	// GetByUsername returns a user by their canonical (lowercased) username.
	// The caller is responsible for canonicalizing the argument (e.g. via
	// ValidateUsername) — the lookup is exact against the stored value.
	// Returns ErrNotFound if no non-deleted user holds that username.
	GetByUsername(ctx context.Context, username string) (*User, error)

	// GetByEmail returns a user by their email address. Email lookup
	// is required for OAuth login (find-or-create by email). Returns
	// ErrNotFound if no user exists with that email, or if they have
	// been soft-deleted. Email is normalized (lowercase + trim) before lookup.
	GetByEmail(ctx context.Context, email string) (*User, error)

	// Update replaces an existing user. Returns ErrNotFound if the
	// user doesn't exist or is soft-deleted. Email and CreatedAt are
	// preserved from the existing record — email is immutable through
	// this method (changing email requires re-verification, not yet implemented).
	Update(ctx context.Context, u *User) error

	// Delete soft-deletes a user by setting DeletedAt.
	// Returns ErrNotFound if the user doesn't exist or is already deleted.
	Delete(ctx context.Context, id string) error

	// SearchProfiles returns users matching query, ranked by match quality and
	// keyset-paginated. A user matches if their (case-insensitive) username
	// equals query (bucket 0), starts with query (bucket 1), or their
	// display_name contains query as a substring (bucket 2); each user appears
	// once at its best (lowest) bucket. Soft-deleted users are excluded, and a
	// NULL username never matches the username predicates (but the row is still
	// matchable by display name). query is lowercased/trimmed here for safety;
	// an empty query yields an empty result (no error). Results are ordered
	// (bucket ASC, SortKey ASC, id ASC) where SortKey =
	// COALESCE(lower(username), lower(display_name)). limit+1 is fetched
	// internally to compute the next *SearchCursor (nil when exhausted), exactly
	// like the timeline feed's next-cursor detection.
	//
	// NOTE: follower_count is deliberately NOT a search-ordering signal even
	// though the SOW lists it as a tiebreak. Coupling the users-table search to
	// the follows table would diverge the two backends and complicate the query
	// for no benefit at this scale; the SOW flags search ranking as
	// revisitable. follower_count is still returned by the profile endpoint —
	// it is just not used to order search results.
	SearchProfiles(ctx context.Context, query string, limit int, after *SearchCursor) ([]*User, *SearchCursor, error)
}
