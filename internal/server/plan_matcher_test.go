package server

import (
	"context"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	plannedworkout "github.com/Prog-Strength/prog-strength-api/internal/planned_workout"
)

// These tests lock the completion-REVERT symmetry between the two plan-matcher
// wrappers. A session can be deleted through either surface — the strength
// handler fires workoutPlanMatcher, the activity handler fires
// activityPlanMatcher — and the revert lookup is keyed by session id alone (ids
// are globally unique in the unified activities base), so every wrapper reverts
// every completion. The two wrappers are now identical in body (no session
// kind); these tests are what guarantee they stay symmetric.

// newPlanEnv builds a real planned-workout repo + service over a migrated
// SQLite DB and returns them with a seeded plan completed by sessionID.
func newPlanEnv(t *testing.T, kind plannedworkout.ActivityKind, sessionID string) (*plannedworkout.Service, plannedworkout.Repository, string) {
	t.Helper()
	repo := plannedworkout.NewSQLiteRepository(dbtest.New(t))
	svc := plannedworkout.NewService(repo)

	start := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	pw := &plannedworkout.PlannedWorkout{
		UserID:            "u1",
		ActivityKind:      kind,
		ScheduledStartUTC: start,
		ScheduledEndUTC:   start.Add(time.Hour),
		Timezone:          "UTC",
	}
	if err := repo.Create(context.Background(), pw); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := repo.SetCompletion(context.Background(), "u1", pw.ID, sessionID); err != nil {
		t.Fatalf("set completion: %v", err)
	}
	return svc, repo, pw.ID
}

// assertReverted fails unless the plan is back to planned with its completion
// link cleared.
func assertReverted(t *testing.T, repo plannedworkout.Repository, planID string) {
	t.Helper()
	got, err := repo.Get(context.Background(), "u1", planID)
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if got.Status != plannedworkout.StatusPlanned {
		t.Errorf("status = %q, want planned (reverted)", got.Status)
	}
	if got.CompletedSessionID != nil {
		t.Errorf("completion link = %v, want cleared", got.CompletedSessionID)
	}
}

// A lift completion must revert when the lift is deleted via the workout matcher
// path.
func TestPlanRevert_LiftCompletion_ViaWorkoutDelete(t *testing.T) {
	svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindLift, "lift-1")

	m := &workoutPlanMatcher{svc: svc}
	m.OnSessionDeleted(context.Background(), "u1", "lift-1")

	assertReverted(t, repo, planID)
}

// A lift completion must also revert when the delete comes through the activity
// matcher path — the lookup is keyed by session id alone.
func TestPlanRevert_LiftCompletion_ViaActivityDelete(t *testing.T) {
	svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindLift, "lift-2")

	m := &activityPlanMatcher{svc: svc}
	m.OnSessionDeleted(context.Background(), "u1", "lift-2")

	assertReverted(t, repo, planID)
}

// A run completion reverts through the activity matcher — its own delete path —
// and, because the lookup is keyed by session id alone, through the workout
// matcher too.
func TestPlanRevert_RunCompletion_BothPaths(t *testing.T) {
	t.Run("activity delete path", func(t *testing.T) {
		svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindRun, "run-1")
		m := &activityPlanMatcher{svc: svc}
		m.OnSessionDeleted(context.Background(), "u1", "run-1")
		assertReverted(t, repo, planID)
	})
	t.Run("workout delete path", func(t *testing.T) {
		svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindRun, "run-2")
		m := &workoutPlanMatcher{svc: svc}
		m.OnSessionDeleted(context.Background(), "u1", "run-2")
		assertReverted(t, repo, planID)
	})
}
