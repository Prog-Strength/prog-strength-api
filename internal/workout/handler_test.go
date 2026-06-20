package workout

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// testActivityRepo builds an activity repository over the shared test DB with
// an in-memory archiver, for wiring the workout handler's TCX-enrichment
// dependency in tests.
func testActivityRepo(d *sql.DB) activity.Repository {
	return activity.NewSQLiteRepository(d, activity.NewMemoryArchiver())
}

// newExerciseHistoryHandler wires a workout handler over ephemeral SQLite
// repos sharing one DB, seeded with the canonical exercise catalog. The
// exercise SQLite repo needs the catalog rows populated via SyncCatalog
// because these handlers read exercise metadata (name, muscle groups).
func newExerciseHistoryHandler(t *testing.T) *Handler {
	t.Helper()
	d := dbtest.New(t)
	exRepo := exercise.NewSQLiteRepository(d)
	if err := exRepo.SyncCatalog(context.Background(), exercise.Catalog); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}
	return NewHandler(NewSQLiteRepository(d), exRepo, testActivityRepo(d))
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
	ctx := context.Background()
	d := dbtest.New(t)
	repo := NewSQLiteRepository(d)
	exRepo := exercise.NewSQLiteRepository(d)
	if err := exRepo.SyncCatalog(ctx, exercise.Catalog); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}
	h := NewHandler(repo, exRepo, testActivityRepo(d))

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

// progressionEnvelope decodes the progression endpoint's success body.
type progressionEnvelope struct {
	Message string                 `json:"message"`
	Data    MuscleGroupProgression `json:"data"`
}

// callProgression issues a progression request with the given raw query
// string (e.g. "movement_pattern=push") and returns the recorder.
func callProgression(t *testing.T, h *Handler, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/workouts/progression?"+rawQuery, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.progression(w, req)
	return w
}

func TestProgressionHandler_MovementPattern_Push(t *testing.T) {
	h := newExerciseHistoryHandler(t)
	w := callProgression(t, h, "movement_pattern=push")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env progressionEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Filter.MovementPattern != "push" {
		t.Errorf("filter.movement_pattern = %q, want push", env.Data.Filter.MovementPattern)
	}
	if env.Data.Filter.MuscleGroup != "" {
		t.Errorf("filter.muscle_group = %q, want empty on pattern path", env.Data.Filter.MuscleGroup)
	}
	want := []string{"chest", "shoulders", "triceps"}
	if got := env.Data.Filter.MuscleGroupsIncluded; !equalStrings(got, want) {
		t.Errorf("muscle_groups_included = %v, want %v", got, want)
	}
	if env.Data.BaselineModel != "recency_weighted_current" {
		t.Errorf("baseline_model = %q", env.Data.BaselineModel)
	}
}

func TestProgressionHandler_MovementPattern_All(t *testing.T) {
	h := newExerciseHistoryHandler(t)
	w := callProgression(t, h, "movement_pattern=all")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env progressionEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantGroups := exercise.AllMuscleGroups()
	want := make([]string, len(wantGroups))
	for i, mg := range wantGroups {
		want[i] = string(mg)
	}
	if got := env.Data.Filter.MuscleGroupsIncluded; !equalStrings(got, want) {
		t.Errorf("muscle_groups_included = %v, want every catalog muscle %v", got, want)
	}
}

func TestProgressionHandler_MissingFilter(t *testing.T) {
	h := newExerciseHistoryHandler(t)
	w := callProgression(t, h, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var env errCodeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "missing_filter" {
		t.Errorf("code = %q, want missing_filter", env.Code)
	}
}

func TestProgressionHandler_ConflictingFilters(t *testing.T) {
	h := newExerciseHistoryHandler(t)
	w := callProgression(t, h, "movement_pattern=push&muscle_group=chest")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var env errCodeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "conflicting_filters" {
		t.Errorf("code = %q, want conflicting_filters", env.Code)
	}
}

func TestProgressionHandler_MuscleGroupLegacy(t *testing.T) {
	h := newExerciseHistoryHandler(t)
	w := callProgression(t, h, "muscle_group=chest")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env progressionEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Filter.MuscleGroup != "chest" {
		t.Errorf("filter.muscle_group = %q, want chest", env.Data.Filter.MuscleGroup)
	}
	if env.Data.Filter.MovementPattern != "" {
		t.Errorf("filter.movement_pattern = %q, want empty on muscle_group path", env.Data.Filter.MovementPattern)
	}
	if got := env.Data.Filter.MuscleGroupsIncluded; !equalStrings(got, []string{"chest"}) {
		t.Errorf("muscle_groups_included = %v, want [chest]", got)
	}
}

func TestProgressionHandler_InvalidMuscleGroup(t *testing.T) {
	h := newExerciseHistoryHandler(t)
	w := callProgression(t, h, "muscle_group=not-a-muscle")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestExerciseOneRMHistory_UnknownExercise(t *testing.T) {
	h := newExerciseHistoryHandler(t)

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

type personalRecordsEnvelope struct {
	Message string              `json:"message"`
	Data    []personalRecordDTO `json:"data"`
}

func TestPersonalRecords_RecentEstimated1RMPoints(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	repo := NewSQLiteRepository(d)
	exRepo := exercise.NewSQLiteRepository(d)
	if err := exRepo.SyncCatalog(ctx, exercise.Catalog); err != nil {
		t.Fatalf("SyncCatalog: %v", err)
	}
	h := NewHandler(repo, exRepo, testActivityRepo(d))

	// Two real headline exercises from the curated default
	// (HeadlineExercises, a []string of catalog slugs): train one, leave
	// the other never-trained.
	trained := HeadlineExercises[0]
	neverTrained := HeadlineExercises[1]

	mkWorkout := func(at time.Time, weight float64) {
		w := &Workout{
			UserID:      "u1",
			Name:        "session",
			PerformedAt: at,
			Exercises: []WorkoutExercise{{
				ExerciseID: trained,
				Order:      0,
				Sets:       []Set{{Reps: 3, Weight: weight, Unit: user.WeightUnitPounds}},
			}},
		}
		if err := repo.Create(ctx, w); err != nil {
			t.Fatalf("create workout: %v", err)
		}
	}
	now := time.Now()
	mkWorkout(now.Add(-40*24*time.Hour), 300)
	mkWorkout(now.Add(-20*24*time.Hour), 310)
	mkWorkout(now.Add(-5*24*time.Hour), 320)

	req := httptest.NewRequest("GET", "/personal-records", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.personalRecords(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	var env personalRecordsEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byID := make(map[string]personalRecordDTO, len(env.Data))
	for _, r := range env.Data {
		byID[r.ExerciseID] = r
	}

	tr, ok := byID[trained]
	if !ok {
		t.Fatalf("trained exercise %q missing from response", trained)
	}
	if len(tr.RecentEstimated1RMPoints) == 0 {
		t.Fatalf("trained lift: want a non-empty trend, got %v", tr.RecentEstimated1RMPoints)
	}
	if len(tr.RecentEstimated1RMPoints) > recentEstimated1RMPointCap {
		t.Fatalf("trend exceeds cap %d: %v", recentEstimated1RMPointCap, tr.RecentEstimated1RMPoints)
	}
	pts := tr.RecentEstimated1RMPoints
	for i := 1; i < len(pts); i++ {
		if pts[i] < pts[i-1] {
			t.Fatalf("trend not ascending: %v", pts)
		}
	}
	if tr.CurrentEstimated1RM == nil {
		t.Fatalf("current_estimated_1rm should be set for a trained lift")
	}

	nt, ok := byID[neverTrained]
	if !ok {
		t.Fatalf("never-trained exercise %q missing from response", neverTrained)
	}
	if nt.RecentEstimated1RMPoints != nil {
		t.Fatalf("never-trained lift: want nil trend, got %v", nt.RecentEstimated1RMPoints)
	}
	if nt.CurrentEstimated1RM != nil {
		t.Fatalf("never-trained lift: want nil current_estimated_1rm, got %v", *nt.CurrentEstimated1RM)
	}
}
