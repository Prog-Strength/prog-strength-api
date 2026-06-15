package activity

import (
	"context"
	"time"
)

// SessionRef identifies a freshly logged (or deleted) running activity for the
// plan matcher. StartUTC is the activity start time in UTC, used to bucket the
// session into a local calendar day.
type SessionRef struct {
	SessionID string
	StartUTC  time.Time
}

// PlanMatcher is the seam the activity package depends on to best-effort link a
// logged running activity to the planned workout it completes (and to revert
// that link when the activity is deleted). It is declared here and implemented
// in the server wiring layer so this package never imports planned_workout.
// All methods are best-effort: failures must not affect ingest or delete.
type PlanMatcher interface {
	OnSessionLogged(ctx context.Context, userID string, ref SessionRef)
	OnSessionDeleted(ctx context.Context, userID, sessionID string)
}
