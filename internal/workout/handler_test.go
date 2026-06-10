package workout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// newExerciseHistoryHandler wires a workout handler over in-memory repos
// seeded with the canonical exercise catalog.
func newExerciseHistoryHandler() *Handler {
	return NewHandler(NewMemoryRepository(), exercise.NewMemoryRepository(exercise.Catalog))
}

// withURLParam attaches a chi URL param to the request context.
func withURLParam(req *http.Request, key, val string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add(key, val)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

type exerciseOneRMHistoryEnvelope struct {
	Message string                       `json:"message"`
	Data    exerciseOneRMHistoryResponse `json:"data"`
}

type errCodeEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func TestExerciseOneRMHistory_HappyPath(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, exercise.NewMemoryRepository(exercise.Catalog))
	ctx := context.Background()

	// Two workouts on the same exercise at different dates create two
	// history points; create them out of chronological order to prove the
	// handler emits ascending by performed_at.
	mkWorkout := func(at time.Time, weight float64) {
		w := &Workout{
			UserID:      "u1",
			Name:        "session",
			PerformedAt: at,
			Exercises: []WorkoutExercise{
				{
					ExerciseID: "barbell-bench-press",
					Order:      0,
					Sets:       []Set{{Reps: 3, Weight: weight, Unit: user.WeightUnitPounds}},
				},
			},
		}
		if err := repo.Create(ctx, w); err != nil {
			t.Fatalf("create workout: %v", err)
		}
	}
	later := time.Date(2026, 2, 1, 17, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 1, 1, 17, 0, 0, 0, time.UTC)
	mkWorkout(later, 230)
	mkWorkout(earlier, 225)

	req := httptest.NewRequest("GET", "/personal-records/barbell-bench-press/history", nil)
	req = withURLParam(req, "exercise_id", "barbell-bench-press")
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.exerciseOneRMHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env exerciseOneRMHistoryEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ExerciseID != "barbell-bench-press" {
		t.Errorf("exercise_id = %q", env.Data.ExerciseID)
	}
	if env.Data.ExerciseName == "" {
		t.Error("expected non-empty exercise_name")
	}
	if env.Data.Unit == nil || *env.Data.Unit != "lb" {
		t.Errorf("unit = %v, want lb", env.Data.Unit)
	}
	if len(env.Data.Points) != 2 {
		t.Fatalf("want 2 points, got %d", len(env.Data.Points))
	}
	if env.Data.Points[0].PerformedAt.After(env.Data.Points[1].PerformedAt) {
		t.Errorf("points not ascending by performed_at: %+v", env.Data.Points)
	}
	if !env.Data.Points[0].PerformedAt.Equal(earlier) {
		t.Errorf("first point = %v, want earliest %v", env.Data.Points[0].PerformedAt, earlier)
	}
	if env.Data.Points[0].Estimated1RM <= 0 {
		t.Errorf("estimated_1rm = %v, want > 0", env.Data.Points[0].Estimated1RM)
	}
}

func TestExerciseOneRMHistory_UnknownExercise(t *testing.T) {
	h := newExerciseHistoryHandler()

	req := httptest.NewRequest("GET", "/personal-records/not-a-real-exercise/history", nil)
	req = withURLParam(req, "exercise_id", "not-a-real-exercise")
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.exerciseOneRMHistory(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	var env errCodeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "unknown_exercise_id" {
		t.Errorf("code = %q, want unknown_exercise_id", env.Code)
	}
}
