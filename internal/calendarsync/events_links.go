package calendarsync

import (
	"context"
	"time"
)

// Link kinds. These are the wire values of a marked event's `link.kind`.
const (
	LinkKindPlannedWorkout = "planned_workout"
	LinkKindActivity       = "activity"
)

// EventLink points a Google event back at the Prog Strength row that wrote it.
type EventLink struct {
	Kind string
	ID   string
}

// EventLinkRepository answers "which of these Google event ids are ours, and
// what do they point at".
//
// Marking is an ID-SET LOOKUP and never a title match. Title matching is the
// tempting shortcut and it is wrong in both directions: a user who renames our
// event in Google loses its mark, and a user who names their own event "Upper
// Body Push" gets a deep link to a planned workout that is not theirs.
type EventLinkRepository interface {
	// LinksForUser returns the user's Google event ids mapped to what they
	// point at. from/to bound the planned-workout side by scheduled start;
	// the activity side has no time column and is bounded by user alone.
	// Extra entries are harmless — the map is only ever probed by id.
	LinksForUser(ctx context.Context, userID string, from, to time.Time) (map[string]EventLink, error)
}
