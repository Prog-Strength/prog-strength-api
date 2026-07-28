package server

import (
	"context"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity/strength"
	plannedworkout "github.com/jwallace145/progressive-overload-fitness-tracker/internal/planned_workout"
)

// The two wrappers differ only on the LOGGED side: the SessionKind they pass
// routes which plan kind (run vs lift) a fresh session completes, and stamps
// completed_session_kind. The DELETED side is kind-agnostic — the revert
// lookup keys on session id alone (see plannedworkout.Service.OnSessionDeleted)
// because the same strength session can carry either recorded kind during the
// unified-model shim period ('workout' from the live write path, 'activity'
// from migration 042's normalization) and be deleted through either surface.

// activityPlanMatcher adapts the shared planned-workout service to the
// activity.PlanMatcher port. A logged activity is always a running activity at
// the hook site, so it completes an "activity"-kind plan.
type activityPlanMatcher struct{ svc *plannedworkout.Service }

var _ activity.PlanMatcher = (*activityPlanMatcher)(nil)

func (m *activityPlanMatcher) OnSessionLogged(ctx context.Context, userID string, ref activity.SessionRef) {
	m.svc.OnSessionLogged(ctx, userID, ref.SessionID, plannedworkout.SessionKindActivity, ref.StartUTC)
}

func (m *activityPlanMatcher) OnSessionDeleted(ctx context.Context, userID, sessionID string) {
	m.svc.OnSessionDeleted(ctx, userID, sessionID)
}

// workoutPlanMatcher adapts the shared planned-workout service to the
// strength.PlanMatcher port. A logged workout completes a "workout"-kind plan.
type workoutPlanMatcher struct{ svc *plannedworkout.Service }

var _ strength.PlanMatcher = (*workoutPlanMatcher)(nil)

func (m *workoutPlanMatcher) OnSessionLogged(ctx context.Context, userID string, ref strength.SessionRef) {
	m.svc.OnSessionLogged(ctx, userID, ref.SessionID, plannedworkout.SessionKindWorkout, ref.StartUTC)
}

func (m *workoutPlanMatcher) OnSessionDeleted(ctx context.Context, userID, sessionID string) {
	m.svc.OnSessionDeleted(ctx, userID, sessionID)
}
