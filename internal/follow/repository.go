package follow

import "context"

// Repository persists the follow graph and its state machine. Implementations
// are in-memory (dev/test default) and SQLite (prod), mirroring the
// dual-implementation pattern of the other domains. All methods are
// context-first.
//
// The mutations enforce the state machine and the actor's side of each edge
// (the row's follower/followee position is the authorization predicate), and
// return the typed errors in errors.go. Followee EXISTENCE is deliberately NOT
// checked here — the handler verifies it via the ProfileProvider before calling
// Request, so the repository stays free of any user-domain dependency.
type Repository interface {
	// --- mutations (state machine) ---

	// Request inserts a pending edge follower → followee. It rejects a
	// self-follow (ErrSelfFollow), a pre-existing row for the ordered pair
	// (ErrAlreadyExists), and a requester already at PendingCap outstanding
	// pending rows (ErrPendingCapExceeded). Followee existence is the handler's
	// concern.
	Request(ctx context.Context, followerID, followeeID string) (Follow, error)

	// Accept flips the pending row addressed to followeeID (authored by
	// followerID) to accepted and stamps accepted_at. Returns ErrNotFound when
	// no such pending row exists.
	Accept(ctx context.Context, followeeID, followerID string) error

	// Reject deletes the pending row addressed to followeeID (authored by
	// followerID). Returns ErrNotFound when no such pending row exists.
	Reject(ctx context.Context, followeeID, followerID string) error

	// Cancel deletes the requester's own pending row (followerID → followeeID).
	// Returns ErrNotFound when no such pending row exists.
	Cancel(ctx context.Context, followerID, followeeID string) error

	// Unfollow deletes the requester's own accepted row (followerID →
	// followeeID). Returns ErrNotFound when no such accepted row exists.
	Unfollow(ctx context.Context, followerID, followeeID string) error

	// RemoveFollower deletes the accepted row where the actor is the followee
	// (followerID → followeeID, status accepted). Returns ErrNotFound when no
	// such accepted row exists.
	RemoveFollower(ctx context.Context, followeeID, followerID string) error

	// Get returns the edge for the ordered (follower, followee) pair regardless
	// of status, or ErrNotFound. It backs the context-sensitive
	// DELETE /follows/{username} teardown (cancel-if-pending / unfollow-if-accepted).
	Get(ctx context.Context, followerID, followeeID string) (Follow, error)

	// --- reads ---

	// CountPending returns the number of outstanding pending rows authored by
	// followerID (the cap check input).
	CountPending(ctx context.Context, followerID string) (int, error)

	// AcceptedFollowees returns the followee ids of viewerID's accepted edges
	// (follower=viewer, status=accepted). This is the feed projection the
	// timeline fan-out (Task 4) consumes.
	AcceptedFollowees(ctx context.Context, viewerID string) ([]string, error)

	// CountFollowers returns the number of accepted edges addressed to userID.
	CountFollowers(ctx context.Context, userID string) (int, error)

	// CountFollowing returns the number of accepted edges authored by userID.
	CountFollowing(ctx context.Context, userID string) (int, error)

	// Relationship returns viewerID's relationship to otherID — one of the five
	// Relationship values.
	Relationship(ctx context.Context, viewerID, otherID string) (Relationship, error)

	// Relationships batch-computes viewerID's relationship to each id in
	// otherIDs, keyed by id, in a single pass (no N+1). Every requested id is
	// present in the map (RelationshipNone when no edge / RelationshipSelf for
	// viewerID itself).
	Relationships(ctx context.Context, viewerID string, otherIDs []string) (map[string]Relationship, error)

	// ListFollowers returns followeeID's accepted followers newest-first
	// (created_at DESC, id DESC), capped at limit, keyset-paginated by before.
	// The returned cursor points at the last row for the next request and is nil
	// when the list is exhausted.
	ListFollowers(ctx context.Context, followeeID string, limit int, before *Cursor) ([]Follow, *Cursor, error)

	// ListFollowing returns followerID's accepted followees newest-first, same
	// pagination contract as ListFollowers.
	ListFollowing(ctx context.Context, followerID string, limit int, before *Cursor) ([]Follow, *Cursor, error)

	// ListIncomingRequests returns the pending rows addressed to followeeID
	// (the requests inbox), newest-first, same pagination contract.
	ListIncomingRequests(ctx context.Context, followeeID string, limit int, before *Cursor) ([]Follow, *Cursor, error)

	// ListOutgoingRequests returns the pending rows authored by followerID,
	// newest-first, same pagination contract.
	ListOutgoingRequests(ctx context.Context, followerID string, limit int, before *Cursor) ([]Follow, *Cursor, error)
}
