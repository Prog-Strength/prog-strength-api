package strength

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	"github.com/Prog-Strength/prog-strength-api/internal/exercise"
)

// newUnifiedStack wires the unified /activities surface exactly like
// server.go does: activity handler + registry whose strength descriptor is
// bound to the workout handler's create/update/route machinery. The strength
// handler's Mount now registers only /personal-records* and headline-exercise
// routes — the legacy /workouts shims were removed in stage 5. These tests live
// here (not in internal/activity) because the parent package must not import
// this child.
func newUnifiedStack(t *testing.T) (http.Handler, *SQLiteRepository, *activity.SQLiteRepository) {
	t.Helper()
	d := dbtest.New(t)
	seedExerciseCatalog(t, d, "back-squat", "barbell-bench-press")
	exRepo := exercise.NewSQLiteRepository(d)

	repo := NewSQLiteRepository(d)
	actRepo := activity.NewSQLiteRepository(d, activity.NewMemoryArchiver())

	wh := NewHandler(repo, exRepo, actRepo)
	ah := activity.NewHandler(actRepo)

	strengthDesc := NewDescriptor(repo)
	strengthDesc.Create = wh.CreateSession
	strengthDesc.Update = wh.UpdateSession
	strengthDesc.MountRoutes = wh.MountActivityRoutes

	ah.SetRegistry(activity.NewRegistry(
		activity.NewEnduranceDescriptor(activity.ActivityRunning, activity.NewSQLiteEnduranceDetailStore(d, activity.ActivityRunning)),
		strengthDesc,
	))

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), "u1")))
		})
	})
	wh.Mount(r)
	ah.Mount(r)
	return r, repo, actRepo
}

func doReq(t *testing.T, srv http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// unifiedItem decodes just the unified-surface fields these tests assert on.
type unifiedItem struct {
	ID           string  `json:"id"`
	ActivityType string  `json:"activity_type"`
	Name         *string `json:"name"`
	Summary      *struct {
		Title    string   `json:"title"`
		Subtitle string   `json:"subtitle"`
		Metrics  []string `json:"metrics"`
	} `json:"summary"`
	Details json.RawMessage `json:"details"`
}

func decodeItem(t *testing.T, w *httptest.ResponseRecorder) unifiedItem {
	t.Helper()
	var env struct {
		Data unifiedItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, w.Body.String())
	}
	return env.Data
}

func seedWorkout(t *testing.T, repo *SQLiteRepository, start time.Time) *Workout {
	t.Helper()
	w := &Workout{
		UserID:      "u1",
		Name:        "Leg day",
		PerformedAt: start,
		Exercises: []WorkoutExercise{{
			ExerciseID: "back-squat",
			Order:      0,
			Sets:       []Set{{Reps: 5, Weight: 225, Unit: "lb"}, {Reps: 5, Weight: 225, Unit: "lb"}},
		}},
	}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("create workout: %v", err)
	}
	return w
}

// TestUnifiedList_IncludesStrength covers scenario (a): GET /activities now
// includes strength sessions, each carrying activity_type and a summary
// rendered from its exercises.
func TestUnifiedList_IncludesStrength(t *testing.T) {
	srv, repo, actRepo := newUnifiedStack(t)
	ctx := context.Background()

	seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	run := &activity.Activity{
		UserID:           "u1",
		ActivityType:     activity.ActivityRunning,
		IngestSource:     activity.IngestManualTCX,
		SourceActivityID: "run1",
		StartTime:        time.Date(2026, 7, 2, 7, 0, 0, 0, time.UTC),
		DistanceMeters:   8046.7,
		DurationSeconds:  2472,
	}
	if err := actRepo.Create(ctx, run, []byte("run")); err != nil {
		t.Fatalf("create run: %v", err)
	}

	w := doReq(t, srv, "GET", "/activities", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Activities []unifiedItem `json:"activities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list: %v; body=%s", err, w.Body.String())
	}
	if len(env.Data.Activities) != 2 {
		t.Fatalf("list = %d items, want 2 (run + lift)", len(env.Data.Activities))
	}
	var lift *unifiedItem
	for i := range env.Data.Activities {
		if env.Data.Activities[i].ActivityType == "strength_training" {
			lift = &env.Data.Activities[i]
		}
	}
	if lift == nil {
		t.Fatalf("no strength_training item in %+v", env.Data.Activities)
	}
	if lift.Summary == nil {
		t.Fatal("strength item has no summary")
	}
	if lift.Summary.Title != "Leg day" {
		t.Errorf("summary title = %q, want Leg day", lift.Summary.Title)
	}
	// The summary is rendered from the exercises/sets (the hydrator's card
	// vocabulary), which requires the batched detail load — not a bare base row.
	joined := strings.Join(lift.Summary.Metrics, "|")
	if !strings.Contains(joined, "1 exercise") || !strings.Contains(joined, "2 sets") {
		t.Errorf("summary metrics = %v, want exercise + set counts", lift.Summary.Metrics)
	}

	// ?type=strength_training filters to the lift.
	w = doReq(t, srv, "GET", "/activities?type=strength_training", "")
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode filtered list: %v", err)
	}
	if len(env.Data.Activities) != 1 || env.Data.Activities[0].ActivityType != "strength_training" {
		t.Fatalf("?type=strength_training = %+v, want just the lift", env.Data.Activities)
	}
}

// TestUnifiedGet_StrengthDetails covers scenario (c): the detail read carries
// exercises and sets under details.
func TestUnifiedGet_StrengthDetails(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)
	wkt := seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))

	w := doReq(t, srv, "GET", "/activities/"+wkt.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", w.Code, w.Body.String())
	}
	item := decodeItem(t, w)
	var details struct {
		Exercises []struct {
			ExerciseID string `json:"exercise_id"`
			Sets       []Set  `json:"sets"`
		} `json:"exercises"`
	}
	if err := json.Unmarshal(item.Details, &details); err != nil {
		t.Fatalf("decode details: %v; details=%s", err, string(item.Details))
	}
	if len(details.Exercises) != 1 || details.Exercises[0].ExerciseID != "back-squat" {
		t.Fatalf("details.exercises = %+v, want the back-squat", details.Exercises)
	}
	if len(details.Exercises[0].Sets) != 2 || details.Exercises[0].Sets[0].Weight != 225 {
		t.Fatalf("sets = %+v, want 2x5@225", details.Exercises[0].Sets)
	}
}

// TestUnifiedCreate_Strength covers scenario (d): POST /activities with a
// strength payload routes through the strength repo path, so the session is
// readable via the strength repo and the PR/1RM side effects fire.
func TestUnifiedCreate_Strength(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)
	ctx := context.Background()

	w := doReq(t, srv, "POST", "/activities", `{
		"activity_type":"strength_training",
		"start_time":"2026-07-01T10:00:00Z",
		"duration_seconds":3600,
		"name":"Push day",
		"details":{"exercises":[{"exercise_id":"barbell-bench-press","sets":[{"reps":5,"weight":185,"unit":"lb"}]}]}
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}
	created := decodeItem(t, w)
	if created.ID == "" {
		t.Fatal("no id on created session")
	}

	// Assert via unified GET.
	w = doReq(t, srv, "GET", "/activities/"+created.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("unified get = %d; body=%s", w.Code, w.Body.String())
	}
	item := decodeItem(t, w)
	if !strings.Contains(string(item.Details), "barbell-bench-press") {
		t.Errorf("details = %s, want the bench press", string(item.Details))
	}

	// The session is a real workout: the strength repo reads it...
	wkt, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if wkt.Name != "Push day" || len(wkt.Exercises) != 1 {
		t.Fatalf("workout = %+v, want Push day with 1 exercise", wkt)
	}
	if wkt.EndedAt == nil || !wkt.EndedAt.Equal(wkt.PerformedAt.Add(time.Hour)) {
		t.Errorf("ended_at = %v, want performed_at + 1h", wkt.EndedAt)
	}

	// ...and the PR + 1RM history side effects fired.
	prs, err := repo.ListPersonalRecords(ctx, "u1")
	if err != nil {
		t.Fatalf("list PRs: %v", err)
	}
	if len(prs) != 1 || prs[0].ExerciseID != "barbell-bench-press" {
		t.Fatalf("PRs = %+v, want a bench PR", prs)
	}
	hist, err := repo.ListOneRepMaxHistory(ctx, "u1", "barbell-bench-press", nil, nil)
	if err != nil {
		t.Fatalf("1RM history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("1RM history = %d rows, want 1", len(hist))
	}
}

// TestUnifiedPutDelete_Strength covers the lift half of scenario (f): PUT is
// full replacement through the strength update path (PRs recompute) and
// DELETE soft-deletes the session.
func TestUnifiedPutDelete_Strength(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)
	ctx := context.Background()
	wkt := seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))

	w := doReq(t, srv, "PUT", "/activities/"+wkt.ID, `{
		"activity_type":"strength_training",
		"start_time":"2026-07-01T10:00:00Z",
		"name":"Leg day v2",
		"details":{"exercises":[{"exercise_id":"back-squat","sets":[{"reps":3,"weight":275,"unit":"lb"}]}]}
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d; body=%s", w.Code, w.Body.String())
	}
	updated, err := repo.GetByID(ctx, wkt.ID)
	if err != nil {
		t.Fatalf("GetByID after put: %v", err)
	}
	if updated.Name != "Leg day v2" {
		t.Errorf("name = %q, want Leg day v2", updated.Name)
	}
	if len(updated.Exercises) != 1 || len(updated.Exercises[0].Sets) != 1 || updated.Exercises[0].Sets[0].Weight != 275 {
		t.Fatalf("exercises after put = %+v, want 1x3@275", updated.Exercises)
	}
	prs, err := repo.ListPersonalRecords(ctx, "u1")
	if err != nil {
		t.Fatalf("list PRs: %v", err)
	}
	if len(prs) != 1 || prs[0].Weight != 275 {
		t.Fatalf("PRs after put = %+v, want the 275 squat", prs)
	}

	w = doReq(t, srv, "DELETE", "/activities/"+wkt.ID, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d; body=%s", w.Code, w.Body.String())
	}
	if _, err := repo.GetByID(ctx, wkt.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID after delete = %v, want ErrNotFound", err)
	}
}

// TestUnifiedStrengthRoutes: the strength descriptor mounts its
// type-specific routes under /activities — progression and TCX enrichment.
// The legacy /workouts aliases were removed in the stage-5 cleanup, so the
// canonical /activities path is the only one that answers.
func TestUnifiedStrengthRoutes(t *testing.T) {
	srv, _, _ := newUnifiedStack(t)

	// Progression lives under /activities (shape unchanged)...
	w := doReq(t, srv, "GET", "/activities/progression?movement_pattern=push", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /activities/progression = %d; body=%s", w.Code, w.Body.String())
	}
	// ...and the removed /workouts alias is gone (404, not routed).
	w = doReq(t, srv, "GET", "/workouts/progression?movement_pattern=push", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /workouts/progression = %d, want 404 (shim removed); body=%s", w.Code, w.Body.String())
	}

	// TCX enrichment is mounted at /activities/{id}/tcx: a bogus id without a
	// multipart body must reach the handler (404 workout-not-found), not fall
	// through the router.
	w = doReq(t, srv, "POST", "/activities/nope/tcx", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "workout not found") {
		t.Fatalf("POST /activities/{id}/tcx = %d body=%s, want the strength handler's 404", w.Code, w.Body.String())
	}
	w = doReq(t, srv, "DELETE", "/activities/nope/tcx", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("DELETE /activities/{id}/tcx = %d, want 404 from the handler", w.Code)
	}
}
