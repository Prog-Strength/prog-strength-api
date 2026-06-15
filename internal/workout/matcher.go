package workout

import (
	"context"
	"time"
)

// SessionRef identifies a freshly logged (or deleted) lifting workout for the
// plan matcher. StartUTC is the workout's PerformedAt in UTC.
type SessionRef struct {
	SessionID string
	StartUTC  time.Time
}

// PlanMatcher is the seam the workout package depends on to best-effort link a
// logged lift to the planned workout it completes (and revert on delete).
// Declared here, implemented in server wiring so this package never imports
// planned_workout. Best-effort: failures must not affect create or delete.
type PlanMatcher interface {
	OnSessionLogged(ctx context.Context, userID string, ref SessionRef)
	OnSessionDeleted(ctx context.Context, userID, sessionID string)
}
