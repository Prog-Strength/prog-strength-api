package server

import (
	"context"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	plannedworkout "github.com/jwallace145/progressive-overload-fitness-tracker/internal/planned_workout"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// activityPlanMatcher adapts the shared planned-workout service to the
// activity.PlanMatcher port. A logged activity is always a running activity at
// the hook site, so it completes an "activity"-kind plan.
type activityPlanMatcher struct{ svc *plannedworkout.Service }

var _ activity.PlanMatcher = (*activityPlanMatcher)(nil)

func (m *activityPlanMatcher) OnSessionLogged(ctx context.Context, userID string, ref activity.SessionRef) {
	m.svc.OnSessionLogged(ctx, userID, ref.SessionID, plannedworkout.SessionKindActivity, ref.StartUTC)
}

func (m *activityPlanMatcher) OnSessionDeleted(ctx context.Context, userID, sessionID string) {
	m.svc.OnSessionDeleted(ctx, userID, sessionID, plannedworkout.SessionKindActivity)
}

// workoutPlanMatcher adapts the shared planned-workout service to the
// workout.PlanMatcher port. A logged workout completes a "workout"-kind plan.
type workoutPlanMatcher struct{ svc *plannedworkout.Service }

var _ workout.PlanMatcher = (*workoutPlanMatcher)(nil)

func (m *workoutPlanMatcher) OnSessionLogged(ctx context.Context, userID string, ref workout.SessionRef) {
	m.svc.OnSessionLogged(ctx, userID, ref.SessionID, plannedworkout.SessionKindWorkout, ref.StartUTC)
}

func (m *workoutPlanMatcher) OnSessionDeleted(ctx context.Context, userID, sessionID string) {
	m.svc.OnSessionDeleted(ctx, userID, sessionID, plannedworkout.SessionKindWorkout)
}
