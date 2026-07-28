package server

import (
	"context"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	plannedworkout "github.com/jwallace145/progressive-overload-fitness-tracker/internal/planned_workout"
)

// These tests lock the completion-REVERT symmetry between the two plan-matcher
// wrappers. Strength sessions can be deleted through either surface — DELETE
// /workouts/{id} fires workoutPlanMatcher, DELETE /activities/{id} fires
// activityPlanMatcher — and the stored completed_session_kind for the same
// lift differs by era: migration 042 normalized pre-existing links to
// 'activity' while the live workout write path still records 'workout'. The
// revert lookup is therefore keyed by session id alone (ids are globally
// unique in the unified activities base), so every wrapper reverts every
// completion regardless of which kind value was recorded.

// newPlanEnv builds a real planned-workout repo + service over a migrated
// SQLite DB and returns them with a seeded plan completed by sessionID with
// the given recorded kind.
func newPlanEnv(t *testing.T, kind plannedworkout.ActivityKind, sessionKind plannedworkout.SessionKind, sessionID string) (*plannedworkout.Service, plannedworkout.Repository, string) {
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
	if err := repo.SetCompletion(context.Background(), "u1", pw.ID, sessionID, sessionKind); err != nil {
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
	if got.CompletedSessionID != nil || got.CompletedSessionKind != nil {
		t.Errorf("completion link = (%v, %v), want cleared", got.CompletedSessionID, got.CompletedSessionKind)
	}
}

// A lift completion recorded as kind='activity' (what migration 042
// normalized every pre-unification link to) must still revert when the lift
// is deleted via DELETE /workouts/{id} — the workout matcher path.
func TestPlanRevert_MigratedLiftCompletion_ViaWorkoutDelete(t *testing.T) {
	svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindLift, plannedworkout.SessionKindActivity, "lift-1")

	m := &workoutPlanMatcher{svc: svc}
	m.OnSessionDeleted(context.Background(), "u1", "lift-1")

	assertReverted(t, repo, planID)
}

// A lift completion recorded as kind='workout' (what the live workout write
// path records) must still revert when the lift is deleted via DELETE
// /activities/{id} — the activity matcher path.
func TestPlanRevert_NewLiftCompletion_ViaActivityDelete(t *testing.T) {
	svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindLift, plannedworkout.SessionKindWorkout, "lift-2")

	m := &activityPlanMatcher{svc: svc}
	m.OnSessionDeleted(context.Background(), "u1", "lift-2")

	assertReverted(t, repo, planID)
}

// A run completion (always recorded kind='activity') reverts through the
// activity matcher — its own delete path — and, because the lookup is keyed
// by session id alone, through the workout matcher too.
func TestPlanRevert_RunCompletion_BothPaths(t *testing.T) {
	t.Run("activity delete path", func(t *testing.T) {
		svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindRun, plannedworkout.SessionKindActivity, "run-1")
		m := &activityPlanMatcher{svc: svc}
		m.OnSessionDeleted(context.Background(), "u1", "run-1")
		assertReverted(t, repo, planID)
	})
	t.Run("workout delete path", func(t *testing.T) {
		svc, repo, planID := newPlanEnv(t, plannedworkout.ActivityKindRun, plannedworkout.SessionKindActivity, "run-2")
		m := &workoutPlanMatcher{svc: svc}
		m.OnSessionDeleted(context.Background(), "u1", "run-2")
		assertReverted(t, repo, planID)
	})
}
