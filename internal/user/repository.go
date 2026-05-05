package user

import "context"

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
}
