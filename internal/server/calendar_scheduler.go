package server

import (
	"context"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarsync"
)

// calendarScheduler adapts the two calendar services to the single
// plannedworkout.CalendarScheduler interface the planned-workout handler
// consumes.
//
// The split exists because a plan's calendar life has two halves with
// genuinely different owners. While a workout is still PLANNED, the plan
// authors its event (Schedule/Resync/Delete) — that is calendarsync.Service.
// The moment a logged session completes it, authorship transfers to the
// activity, which re-renders the same event through its type's renderer —
// that is calendarsync.ActivityService.
//
// Composing them here, in the wiring layer, keeps that transfer explicit and
// keeps the two services from having to know about each other. It is the same
// move timelinePublisher and plan_matcher already make in this package:
// cross-domain seams live in server, not inside either domain.
type planCalendarScheduler struct {
	plans      *calendarsync.Service
	activities *calendarsync.ActivityService
}

func (c planCalendarScheduler) Schedule(ctx context.Context, userID, planID, detailOverride string) error {
	return c.plans.Schedule(ctx, userID, planID, detailOverride)
}

func (c planCalendarScheduler) Resync(ctx context.Context, userID, planID string) error {
	return c.plans.Resync(ctx, userID, planID)
}

func (c planCalendarScheduler) Delete(ctx context.Context, userID, planID string) error {
	return c.plans.Delete(ctx, userID, planID)
}

// SyncCompletedActivity routes to the activity service, which owns completed
// sessions. When activity sync is unconfigured this is a no-op rather than an
// error: the plan is still correctly marked completed in Prog Strength, and
// Google simply keeps showing the planned event.
func (c planCalendarScheduler) SyncCompletedActivity(ctx context.Context, userID, planID, activityID string) error {
	if c.activities == nil {
		return nil
	}
	return c.activities.SyncCompletedActivity(ctx, userID, planID, activityID)
}
