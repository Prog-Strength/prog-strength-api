package workout

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// fakePlanMatcher records the OnSessionLogged refs and OnSessionDeleted ids it
// receives so handler tests can assert the create/delete hooks fired.
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

// doCreate drives the create handler with a JSON body for testUserID and
// returns the recorder plus the decoded created workout.
func doCreate(t *testing.T, h *Handler, performedAt time.Time) (*httptest.ResponseRecorder, *Workout) {
	t.Helper()
	body := map[string]any{
		"name":         "session",
		"performed_at": performedAt.Format(time.RFC3339),
		"exercises": []map[string]any{
			{
				"exercise_id": "barbell-bench-press",
				"sets": []map[string]any{
					{"reps": 5, "weight": 135.0, "unit": user.WeightUnitPounds},
				},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest("POST", "/workouts", bytes.NewReader(raw))
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data Workout `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return w, &env.Data
}

// TestPlanMatcher_CreateFiresOnSessionLogged proves creating a workout calls
// OnSessionLogged once with the new workout id and its PerformedAt.
func TestPlanMatcher_CreateFiresOnSessionLogged(t *testing.T) {
	h := NewHandler(NewMemoryRepository(), exercise.NewMemoryRepository(exercise.Catalog))
	fake := &fakePlanMatcher{}
	h.SetPlanMatcher(fake)

	performedAt := time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC)
	_, created := doCreate(t, h, performedAt)

	if len(fake.logged) != 1 {
		t.Fatalf("OnSessionLogged calls = %d, want 1", len(fake.logged))
	}
	call := fake.logged[0]
	if call.userID != "u1" {
		t.Errorf("logged userID = %q, want u1", call.userID)
	}
	if call.ref.SessionID != created.ID {
		t.Errorf("logged SessionID = %q, want %q", call.ref.SessionID, created.ID)
	}
	if !call.ref.StartUTC.Equal(performedAt) {
		t.Errorf("logged StartUTC = %v, want %v", call.ref.StartUTC, performedAt)
	}
}

// TestPlanMatcher_DeleteFiresOnSessionDeleted proves deleting a workout calls
// OnSessionDeleted with that workout id and the owning user.
func TestPlanMatcher_DeleteFiresOnSessionDeleted(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, exercise.NewMemoryRepository(exercise.Catalog))
	fake := &fakePlanMatcher{}
	h.SetPlanMatcher(fake)

	w := &Workout{
		UserID:      "u1",
		Name:        "session",
		PerformedAt: time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{
			{
				ExerciseID: "barbell-bench-press",
				Order:      0,
				Sets:       []Set{{Reps: 5, Weight: 135, Unit: user.WeightUnitPounds}},
			},
		},
	}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("seed workout: %v", err)
	}

	del := httptest.NewRequest("DELETE", "/workouts/"+w.ID, nil)
	del = withURLParam(del.WithContext(authctx.WithUserID(del.Context(), "u1")), "id", w.ID)
	wd := httptest.NewRecorder()
	h.delete(wd, del)
	if wd.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", wd.Code, wd.Body.String())
	}

	if len(fake.deleted) != 1 {
		t.Fatalf("OnSessionDeleted calls = %d, want 1", len(fake.deleted))
	}
	call := fake.deleted[0]
	if call.userID != "u1" {
		t.Errorf("deleted userID = %q, want u1", call.userID)
	}
	if call.sessionID != w.ID {
		t.Errorf("deleted sessionID = %q, want %q", call.sessionID, w.ID)
	}
}

// TestPlanMatcher_NilIsNoOp proves the nil-safe path: create + delete with no
// matcher set must not panic.
func TestPlanMatcher_NilIsNoOp(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, exercise.NewMemoryRepository(exercise.Catalog))
	// no SetPlanMatcher call — planMatcher stays nil.

	_, created := doCreate(t, h, time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC))

	del := httptest.NewRequest("DELETE", "/workouts/"+created.ID, nil)
	del = withURLParam(del.WithContext(authctx.WithUserID(del.Context(), "u1")), "id", created.ID)
	wd := httptest.NewRecorder()
	h.delete(wd, del)
	if wd.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", wd.Code, wd.Body.String())
	}
}
