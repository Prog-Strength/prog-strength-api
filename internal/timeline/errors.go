package timeline

import "errors"

var (
	// ErrNotFound is returned when a post (or comment/reaction under it) does
	// not exist OR is not viewable by the caller. The two cases are
	// deliberately indistinguishable so post ids can't be enumerated
	// cross-user — a viewer who can't see a post gets the same 404 as one
	// asking for a post that was never created. This is the not-viewable
	// half of the canView/canModerate authorization split.
	ErrNotFound = errors.New("timeline: not found")

	// ErrValidation is returned for bad client input that never reaches
	// storage: an empty or over-long comment body, or an unknown reaction
	// type. The handler maps it to a 400.
	ErrValidation = errors.New("timeline: validation failed")

	// ErrStorage is returned when an underlying storage operation fails for a
	// reason that isn't a clean not-found or validation case (a failed insert
	// of a reaction/comment, a scan error). The handler maps it to a 500.
	ErrStorage = errors.New("timeline: storage failed")
)
