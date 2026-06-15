package beta

import "context"

// Repository persists the beta allowlist. Implementations may be backed by
// SQLite or in-memory storage. It embeds Checker (the read path the auth
// gate uses) plus the admin write/list methods.
type Repository interface {
	Checker

	// Add inserts the normalized email into the allowlist. It is idempotent:
	// re-adding an existing email is not an error and does not duplicate or
	// overwrite the original row. addedBy and note are stored as SQL NULL
	// when empty.
	Add(ctx context.Context, email, addedBy, note string) error

	// Remove deletes the normalized email from the allowlist. It returns
	// true if a row was removed, false if the email was not present.
	Remove(ctx context.Context, email string) (bool, error)

	// List returns every row, ordered by added_at ascending.
	List(ctx context.Context) ([]AllowedEmail, error)
}
