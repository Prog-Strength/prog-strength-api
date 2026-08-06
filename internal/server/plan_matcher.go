package server

import (
	"context"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/activity/strength"
	plannedworkout "github.com/Prog-Strength/prog-strength-api/internal/planned_workout"
)

// The two adapters below bridge the activity and strength packages' PlanMatcher
// ports (each with its own SessionRef type) onto the one shared planned-workout
// service. They are now identical in body: neither passes a session kind. Which
// plan kind (lift vs run) a logged session completes is derived inside the
// service from the completing activity's activity_type (see the
// activityKindResolver below), so the logged-side no longer needs to say. Two
// adapters remain only because Go can't have one struct satisfy two interface
// types whose method shares a name but differs in parameter type.

// activityPlanMatcher adapts the shared planned-workout service to the
// activity.PlanMatcher port.
type activityPlanMatcher struct{ svc *plannedworkout.Service }

var _ activity.PlanMatcher = (*activityPlanMatcher)(nil)

func (m *activityPlanMatcher) OnSessionLogged(ctx context.Context, userID string, ref activity.SessionRef) {
	m.svc.OnSessionLogged(ctx, userID, ref.SessionID, ref.StartUTC)
}

func (m *activityPlanMatcher) OnSessionDeleted(ctx context.Context, userID, sessionID string) {
	m.svc.OnSessionDeleted(ctx, userID, sessionID)
}

// workoutPlanMatcher adapts the shared planned-workout service to the
// strength.PlanMatcher port.
type workoutPlanMatcher struct{ svc *plannedworkout.Service }

var _ strength.PlanMatcher = (*workoutPlanMatcher)(nil)

func (m *workoutPlanMatcher) OnSessionLogged(ctx context.Context, userID string, ref strength.SessionRef) {
	m.svc.OnSessionLogged(ctx, userID, ref.SessionID, ref.StartUTC)
}

func (m *workoutPlanMatcher) OnSessionDeleted(ctx context.Context, userID, sessionID string) {
	m.svc.OnSessionDeleted(ctx, userID, sessionID)
}

// activityKindResolver implements plannedworkout.ActivityKindResolver over the
// activities base repository: it looks up the completing session by id and maps
// its activity_type to the plan kind it can complete — strength_training → lift,
// every endurance type → run. This is what lets the auto-matcher route a logged
// session to a lift or run plan without a session-kind discriminator.
type activityKindResolver struct{ repo activity.Repository }

var _ plannedworkout.ActivityKindResolver = (*activityKindResolver)(nil)

func (r *activityKindResolver) ResolvePlanKind(ctx context.Context, userID, sessionID string) (plannedworkout.ActivityKind, error) {
	a, err := r.repo.Get(ctx, userID, sessionID)
	if err != nil {
		return "", err
	}
	if a.ActivityType == activity.ActivityStrengthTraining {
		return plannedworkout.ActivityKindLift, nil
	}
	return plannedworkout.ActivityKindRun, nil
}
