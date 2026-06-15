package follow

import "time"

// Status is the lifecycle state of a follow edge. A request lands as
// StatusPending and becomes StatusAccepted when the followee accepts; the
// reject/cancel/unfollow/remove transitions delete the row rather than moving
// it to a terminal state, so these two values are the whole machine.
type Status string

const (
	StatusPending  Status = "pending"
	StatusAccepted Status = "accepted"
)

// Follow is one directed (follower → followee) edge in the graph. A single row
// models the relationship for an ordered pair from request through acceptance.
// AcceptedAt is nil while pending and stamped on acceptance.
type Follow struct {
	ID         string
	FollowerID string
	FolloweeID string
	Status     Status
	CreatedAt  time.Time
	AcceptedAt *time.Time
}

// Relationship is the viewer-relative state of another user, computed per row
// so every read surface (profile, lists, search) can render the right action
// affordance without extra round-trips. It is the small enum the SOW pins:
// none | requested | pending_incoming | following | self.
type Relationship string

const (
	// RelationshipNone: no edge exists between viewer and other.
	RelationshipNone Relationship = "none"
	// RelationshipRequested: viewer → other is pending (viewer asked).
	RelationshipRequested Relationship = "requested"
	// RelationshipPendingIncoming: other → viewer is pending (viewer can accept).
	RelationshipPendingIncoming Relationship = "pending_incoming"
	// RelationshipFollowing: viewer → other is accepted.
	RelationshipFollowing Relationship = "following"
	// RelationshipSelf: viewer and other are the same user.
	RelationshipSelf Relationship = "self"
)

// Cursor is the keyset position for the follow graph's paginated lists and the
// requests inbox: the (created_at, id) pair of the last row on a page. The next
// page asks for rows strictly before this position in (created_at DESC, id DESC)
// order. id is the tiebreaker that makes the cursor a total order so rows
// sharing a created_at paginate without gaps or repeats. It mirrors
// timeline.Cursor; the handler encodes it into the opaque cursor token.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// PendingCap bounds the number of outstanding pending requests a single user
// may author. It is a coarse abuse backstop against unbounded request-spam, NOT
// rate-limiting: there is no per-time-window accounting, just a hard ceiling on
// the live pending count. The number is a domain constant, not a per-user
// setting.
const PendingCap = 200

// mergeRelationship folds one edge into a running relationship for viewerID.
// A row is described by its author (followerID) and status; the edge's other
// endpoint is the user the relationship is about. Both repository backends call
// this so the viewer-relative mapping is defined once:
//   - viewer authored an accepted edge      → following
//   - viewer authored a pending edge        → requested
//   - someone else authored a pending edge addressed to viewer → pending_incoming
//   - someone else's accepted edge addressed to viewer is the inverse direction
//     and does not change the viewer's own action affordance, so it stays as-is.
//
// At most one row exists per direction, and the outbound direction (viewer as
// follower) dominates the inbound one for affordance purposes, so a simple fold
// is sufficient.
func mergeRelationship(cur Relationship, viewerID, followerID string, status Status) Relationship {
	if followerID == viewerID {
		// Outbound edge: viewer → other.
		if status == StatusAccepted {
			return RelationshipFollowing
		}
		return RelationshipRequested
	}
	// Inbound edge: other → viewer. Only a pending inbound request changes the
	// viewer's affordance (they can accept it); an accepted inbound edge means
	// the other user follows the viewer, which doesn't alter the viewer's own
	// follow button. Never downgrade an already-resolved outbound state.
	if status == StatusPending && cur == RelationshipNone {
		return RelationshipPendingIncoming
	}
	return cur
}
