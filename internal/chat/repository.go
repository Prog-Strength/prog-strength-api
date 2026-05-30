package chat

import "context"

// MaxSessionsPerUser is the eviction cap from the SOW. When
// CreateSession would push the user above this number, the repo
// hard-deletes the oldest session (by last_message_at) inside the
// same transaction. Lifted to a package-level constant so tests can
// assert against it.
const MaxSessionsPerUser = 50

// Repository persists chat sessions and their messages. Same
// interface-with-multiple-implementations pattern the rest of the
// domain packages use: an in-memory implementation services tests,
// SQLite services production.
type Repository interface {
	// CreateSession persists a new session and evicts the user's
	// oldest session in the same transaction when the per-user count
	// would exceed MaxSessionsPerUser. The caller supplies the id
	// (client-minted UUID); the repo sets all timestamps.
	//
	// Returns ErrSessionIDExists if the id is already in use,
	// regardless of which user owns the existing row — UUID
	// collisions are vanishingly rare but the constraint is there
	// so callers can't accidentally clobber a row.
	CreateSession(ctx context.Context, s *Session) error

	// GetSession returns the session by id, scoped to the given
	// user. Returns ErrNotFound for missing rows, soft-deleted
	// rows, or rows owned by another user. The three cases are
	// deliberately indistinguishable.
	GetSession(ctx context.Context, userID, sessionID string) (*Session, error)

	// ListSessions returns the user's non-deleted sessions, sorted
	// last_message_at DESC. Capped at MaxSessionsPerUser because
	// the eviction policy guarantees the user never has more than
	// that.
	ListSessions(ctx context.Context, userID string) ([]Session, error)

	// SetTitle updates a session's title and bumps updated_at.
	// Returns ErrNotFound for missing / soft-deleted / wrong-user
	// rows. The caller is responsible for validating the title via
	// NormalizeTitle; the repo trusts what it's given.
	SetTitle(ctx context.Context, userID, sessionID, title string) error

	// SoftDeleteSession sets deleted_at on a session. Messages are
	// untouched — a future restore-from-trash UI can flip the row
	// back. Returns ErrNotFound for missing / already-deleted /
	// wrong-user rows.
	SoftDeleteSession(ctx context.Context, userID, sessionID string) error

	// AppendTurn appends a user message + assistant message pair
	// inside one transaction. Authorizes via userID. Bumps the
	// session's last_message_at + updated_at as part of the same
	// transaction so the eviction sort and the response payload
	// stay consistent.
	//
	// Position is assigned by the repo as (current_max + 1) and
	// (current_max + 2); callers leave the field zero. Returns
	// ErrNotFound for missing / soft-deleted / wrong-user rows.
	AppendTurn(ctx context.Context, userID, sessionID string, turn Turn) (Session, []Message, error)

	// ListMessages returns every message for the session in
	// position-ascending order. Returns ErrNotFound when the
	// session doesn't exist, is soft-deleted, or belongs to
	// another user — the userID check happens before any messages
	// are returned.
	ListMessages(ctx context.Context, userID, sessionID string) ([]Message, error)
}
