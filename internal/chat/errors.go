package chat

import (
	"errors"
	"fmt"
)

var (
	// ErrNotFound is returned when a chat session lookup fails because
	// the id doesn't exist, was soft-deleted, or belongs to another
	// user. The three cases are deliberately indistinguishable —
	// returning "wrong owner" would let attackers enumerate session
	// ids belonging to other users.
	ErrNotFound = errors.New("chat: not found")

	// ErrUserIDRequired surfaces an empty user_id on session create.
	// The handler layer should never let this hit the repo (auth
	// middleware sets the user from the JWT) but the check exists
	// for the in-memory repo's hand-written tests.
	ErrUserIDRequired = errors.New("chat: user ID is required")

	// ErrSessionIDRequired surfaces an empty session id on operations
	// that take one as input (PATCH, DELETE, message append).
	ErrSessionIDRequired = errors.New("chat: session ID is required")

	// ErrInvalidSessionID surfaces non-UUID strings passed as session
	// ids. We accept client-minted UUIDs but reject anything else so
	// the table can't grow with garbage keys.
	ErrInvalidSessionID = errors.New("chat: session ID must be a UUID")

	// ErrSessionIDExists is returned when POST /chat-sessions is
	// called with an id that already belongs to a session. Maps to
	// HTTP 409.
	ErrSessionIDExists = errors.New("chat: session ID already exists")

	// ErrEmptyContent surfaces an empty user or assistant message on
	// append. Validated at the handler boundary; the repo also
	// rejects so the in-memory tests catch regressions.
	ErrEmptyContent = errors.New("chat: message content is required")

	// ErrTitleLength wraps the 1..80 char rule the PATCH endpoint
	// enforces on title updates. Lifted into its own error so the
	// handler can map directly to 400 without string-matching.
	ErrTitleLength = errors.New("chat: title must be 1–80 characters")
)

// InvalidRoleError is returned when a Message.Role value isn't one
// of the closed Role enum values. Carries the bad value so the
// handler can echo it back to the client for debugging.
type InvalidRoleError struct {
	Value string
}

func (e *InvalidRoleError) Error() string {
	return fmt.Sprintf("chat: invalid role %q", e.Value)
}
