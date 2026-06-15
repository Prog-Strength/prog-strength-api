package follow

import "errors"

var (
	// ErrNotFound is returned when a mutation finds no row matching the actor's
	// expected side of the relationship: accepting/rejecting with no pending
	// request addressed to the actor, canceling/unfollowing/removing with no
	// matching row, or a Get with no edge. The handler maps it to 404.
	ErrNotFound = errors.New("follow: not found")

	// ErrInvalidState is returned when a row exists but is in the wrong status
	// for the requested transition (e.g. an addressed-side operation finds the
	// edge but in an unexpected state). The handler maps it to 409.
	ErrInvalidState = errors.New("follow: invalid state")

	// ErrSelfFollow is returned when follower and followee are the same user.
	// The handler maps it to 400.
	ErrSelfFollow = errors.New("follow: cannot follow yourself")

	// ErrAlreadyExists is returned by Request when a row already exists for the
	// ordered (follower, followee) pair — pending or accepted. The handler maps
	// it to 409.
	ErrAlreadyExists = errors.New("follow: relationship already exists")

	// ErrPendingCapExceeded is returned by Request when the requester already
	// holds PendingCap outstanding pending rows. The handler maps it to 429.
	ErrPendingCapExceeded = errors.New("follow: pending request cap exceeded")
)
