package strength

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// fakePlanMatcher records the OnSessionLogged refs and OnSessionDeleted ids it
// receives so handler tests can assert the log hook fired. It still implements
// the full PlanMatcher port (OnSessionDeleted included) even though the strength
// path no longer deletes — deletes flow through the unified /activities handler,
// whose own matcher reverts the plan.
type fakePlanMatcher struct {
	logged  []loggedCall
	deleted []deletedCall
}

type loggedCall struct {
	userID string
	ref    SessionRef
}

type deletedCall struct {
	userID    string
	sessionID string
}

func (f *fakePlanMatcher) OnSessionLogged(_ context.Context, userID string, ref SessionRef) {
	f.logged = append(f.logged, loggedCall{userID: userID, ref: ref})
}

func (f *fakePlanMatcher) OnSessionDeleted(_ context.Context, userID, sessionID string) {
	f.deleted = append(f.deleted, deletedCall{userID: userID, sessionID: sessionID})
}

var _ PlanMatcher = (*fakePlanMatcher)(nil)

// doCreate logs a strength session through CreateSession — the descriptor seam
// the unified POST /activities drives — and returns the new activity id.
func doCreate(t *testing.T, h *Handler, performedAt time.Time) string {
	t.Helper()
	details, err := json.Marshal(map[string]any{
		"exercises": []map[string]any{
			{
				"exercise_id": "barbell-bench-press",
				"sets": []map[string]any{
					{"reps": 5, "weight": 135.0, "unit": user.WeightUnitPounds},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	name := "session"
	id, err := h.CreateSession(context.Background(), "u1", activity.CreateRequest{
		Type:      activity.ActivityStrengthTraining,
		StartTime: performedAt,
		Name:      &name,
		Details:   details,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return id
}

// TestPlanMatcher_CreateFiresOnSessionLogged proves logging a workout calls
// OnSessionLogged once with the new activity id and its start time.
func TestPlanMatcher_CreateFiresOnSessionLogged(t *testing.T) {
	d := dbtest.New(t)
	seedExerciseCatalog(t, d, "barbell-bench-press")
	h := NewHandler(NewSQLiteRepository(d), exercise.NewSQLiteRepository(d), testActivityRepo(d))
	fake := &fakePlanMatcher{}
	h.SetPlanMatcher(fake)

	performedAt := time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC)
	id := doCreate(t, h, performedAt)

	if len(fake.logged) != 1 {
		t.Fatalf("OnSessionLogged calls = %d, want 1", len(fake.logged))
	}
	call := fake.logged[0]
	if call.userID != "u1" {
		t.Errorf("logged userID = %q, want u1", call.userID)
	}
	if call.ref.SessionID != id {
		t.Errorf("logged SessionID = %q, want %q", call.ref.SessionID, id)
	}
	if !call.ref.StartUTC.Equal(performedAt) {
		t.Errorf("logged StartUTC = %v, want %v", call.ref.StartUTC, performedAt)
	}
}

// TestPlanMatcher_NilIsNoOp proves the nil-safe path: logging a workout with no
// matcher set must not panic.
func TestPlanMatcher_NilIsNoOp(t *testing.T) {
	d := dbtest.New(t)
	seedExerciseCatalog(t, d, "barbell-bench-press")
	h := NewHandler(NewSQLiteRepository(d), exercise.NewSQLiteRepository(d), testActivityRepo(d))
	// no SetPlanMatcher call — planMatcher stays nil.

	_ = doCreate(t, h, time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC))
}
