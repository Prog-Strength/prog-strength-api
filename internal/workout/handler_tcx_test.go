package workout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
)

const tcxTestUser = "u1"

// newTCXHandler wires a workout handler with a real activity repository over
// one shared ephemeral DB, so the workout-TCX endpoints exercise the genuine
// cross-domain write paths (activity create, link, soft-delete).
func newTCXHandler(t *testing.T) (*Handler, Repository, activity.Repository) {
	t.Helper()
	d := dbtest.New(t)
	exRepo := exercise.NewSQLiteRepository(d)
	wrepo := NewSQLiteRepository(d)
	arepo := activity.NewSQLiteRepository(d, activity.NewMemoryArchiver())
	return NewHandler(wrepo, exRepo, arepo), wrepo, arepo
}

// strengthFixture reads a TCX fixture from the activity package's testdata.
func strengthFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../activity/testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func tcxMultipart(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "workout.tcx")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// tcxWorkoutEnvelope decodes the success response (httpresp wraps as {data}).
type tcxWorkoutEnvelope struct {
	Data struct {
		ID          string              `json:"id"`
		ActivityID  *string             `json:"activity_id"`
		PerformedAt time.Time           `json:"performed_at"`
		EndedAt     *time.Time          `json:"ended_at"`
		Exercises   []json.RawMessage   `json:"exercises"`
		Enrichment  *enrichmentEnvelope `json:"enrichment"`
	} `json:"data"`
}

type enrichmentEnvelope struct {
	SourceActivityID string `json:"source_activity_id"`
	DurationSeconds  int    `json:"duration_seconds"`
	AvgHeartRateBpm  *int   `json:"avg_heart_rate_bpm"`
	MaxHeartRateBpm  *int   `json:"max_heart_rate_bpm"`
	TotalCalories    *int   `json:"total_calories"`
	Trackpoints      []struct {
		Sequence       int  `json:"sequence"`
		ElapsedSeconds int  `json:"elapsed_seconds"`
		HeartRateBpm   *int `json:"heart_rate_bpm"`
	} `json:"trackpoints"`
}

type dupEnvelope struct {
	Error    string `json:"error"`
	Code     string `json:"code"`
	Existing struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"existing"`
}

func doTCXImport(t *testing.T, h *Handler, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := tcxMultipart(t, data)
	req := httptest.NewRequest("POST", "/workouts/imports", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(authctx.WithUserID(req.Context(), tcxTestUser))
	w := httptest.NewRecorder()
	h.importFromTCX(w, req)
	return w
}

func doTCXAttach(t *testing.T, h *Handler, workoutID string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := tcxMultipart(t, data)
	req := httptest.NewRequest("POST", "/workouts/"+workoutID+"/tcx", body)
	req.Header.Set("Content-Type", ct)
	req = withURLParam(req.WithContext(authctx.WithUserID(req.Context(), tcxTestUser)), "id", workoutID)
	w := httptest.NewRecorder()
	h.attachTCX(w, req)
	return w
}

func doTCXDetach(t *testing.T, h *Handler, workoutID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/workouts/"+workoutID+"/tcx", nil)
	req = withURLParam(req.WithContext(authctx.WithUserID(req.Context(), tcxTestUser)), "id", workoutID)
	w := httptest.NewRecorder()
	h.detachTCX(w, req)
	return w
}

func decodeWorkout(t *testing.T, w *httptest.ResponseRecorder) tcxWorkoutEnvelope {
	t.Helper()
	var env tcxWorkoutEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode workout: %v; body=%s", err, w.Body.String())
	}
	return env
}

func TestImportFromTCX_CreatesEmptyEnrichedWorkout(t *testing.T) {
	h, _, _ := newTCXHandler(t)
	w := doTCXImport(t, h, strengthFixture(t, "strength_session.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	env := decodeWorkout(t, w)
	d := env.Data
	if len(d.Exercises) != 0 {
		t.Errorf("exercises = %d, want 0 (empty workout)", len(d.Exercises))
	}
	if d.ActivityID == nil || *d.ActivityID == "" {
		t.Error("activity_id should be set")
	}
	// performed_at = TCX start (13:12:00Z); ended_at = start + 120 s.
	wantStart := time.Date(2026, 6, 19, 13, 12, 0, 0, time.UTC)
	if !d.PerformedAt.Equal(wantStart) {
		t.Errorf("performed_at = %v, want %v", d.PerformedAt, wantStart)
	}
	if d.EndedAt == nil || !d.EndedAt.Equal(wantStart.Add(120*time.Second)) {
		t.Errorf("ended_at = %v, want %v", d.EndedAt, wantStart.Add(120*time.Second))
	}
	if d.Enrichment == nil {
		t.Fatal("enrichment = nil, want populated")
	}
	if d.Enrichment.AvgHeartRateBpm == nil || *d.Enrichment.AvgHeartRateBpm != 140 {
		t.Errorf("avg_heart_rate_bpm = %v, want 140", d.Enrichment.AvgHeartRateBpm)
	}
	if d.Enrichment.MaxHeartRateBpm == nil || *d.Enrichment.MaxHeartRateBpm != 180 {
		t.Errorf("max_heart_rate_bpm = %v, want 180", d.Enrichment.MaxHeartRateBpm)
	}
	if d.Enrichment.TotalCalories == nil || *d.Enrichment.TotalCalories != 240 {
		t.Errorf("total_calories = %v, want 240", d.Enrichment.TotalCalories)
	}
	if len(d.Enrichment.Trackpoints) == 0 {
		t.Error("enrichment.trackpoints empty on detail, want populated")
	}
}

func TestAttachTCX_SetsLinkWithoutChangingTimes(t *testing.T) {
	h, wrepo, _ := newTCXHandler(t)
	performedAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	endedAt := performedAt.Add(45 * time.Minute)
	wk := &Workout{UserID: tcxTestUser, PerformedAt: performedAt, EndedAt: &endedAt}
	if err := wrepo.Create(context.Background(), wk); err != nil {
		t.Fatalf("create workout: %v", err)
	}

	w := doTCXAttach(t, h, wk.ID, strengthFixture(t, "strength_session.tcx"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	env := decodeWorkout(t, w)
	if env.Data.ActivityID == nil {
		t.Error("activity_id should be set after attach")
	}
	// Attach must NOT touch the user-logged times.
	if !env.Data.PerformedAt.Equal(performedAt) {
		t.Errorf("performed_at = %v, want unchanged %v", env.Data.PerformedAt, performedAt)
	}
	if env.Data.EndedAt == nil || !env.Data.EndedAt.Equal(endedAt) {
		t.Errorf("ended_at = %v, want unchanged %v", env.Data.EndedAt, endedAt)
	}

	// Second attach → 409 workout_tcx_exists.
	w2 := doTCXAttach(t, h, wk.ID, strengthFixture(t, "strength_session.tcx"))
	if w2.Code != http.StatusConflict {
		t.Fatalf("second attach status = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
	var env2 dupEnvelope
	if err := json.Unmarshal(w2.Body.Bytes(), &env2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env2.Code != "workout_tcx_exists" {
		t.Errorf("code = %q, want workout_tcx_exists", env2.Code)
	}
}

func TestAttachTCX_DuplicateActivityPointsToWorkout(t *testing.T) {
	h, wrepo, _ := newTCXHandler(t)
	// First: import the file, which creates an activity + workout.
	imp := doTCXImport(t, h, strengthFixture(t, "strength_session.tcx"))
	if imp.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201; body=%s", imp.Code, imp.Body.String())
	}
	firstWorkoutID := decodeWorkout(t, imp).Data.ID

	// Second: a different workout tries to attach the SAME file → dedup fires.
	wk := &Workout{UserID: tcxTestUser, PerformedAt: time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)}
	if err := wrepo.Create(context.Background(), wk); err != nil {
		t.Fatalf("create workout: %v", err)
	}
	w := doTCXAttach(t, h, wk.ID, strengthFixture(t, "strength_session.tcx"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var env dupEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "duplicate_activity" {
		t.Errorf("code = %q, want duplicate_activity", env.Code)
	}
	if env.Existing.Kind != "workout" {
		t.Errorf("existing.kind = %q, want workout", env.Existing.Kind)
	}
	if env.Existing.ID != firstWorkoutID {
		t.Errorf("existing.id = %q, want first workout %q", env.Existing.ID, firstWorkoutID)
	}
}

func TestDetachTCX_ClearsLinkAndSoftDeletesActivity(t *testing.T) {
	h, wrepo, arepo := newTCXHandler(t)
	imp := doTCXImport(t, h, strengthFixture(t, "strength_session.tcx"))
	if imp.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201", imp.Code)
	}
	env := decodeWorkout(t, imp)
	workoutID := env.Data.ID
	activityID := *env.Data.ActivityID

	w := doTCXDetach(t, h, workoutID)
	if w.Code != http.StatusNoContent {
		t.Fatalf("detach status = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	// activity_id cleared on the workout.
	reloaded, err := wrepo.GetByID(context.Background(), workoutID)
	if err != nil {
		t.Fatalf("reload workout: %v", err)
	}
	if reloaded.ActivityID != nil {
		t.Errorf("activity_id = %v, want nil after detach", reloaded.ActivityID)
	}
	// Linked activity soft-deleted (no longer live).
	if _, err := arepo.Get(context.Background(), tcxTestUser, activityID); !errors.Is(err, activity.ErrNotFound) {
		t.Errorf("activity get err = %v, want ErrNotFound (soft-deleted)", err)
	}

	// Idempotent: a second detach is a 204 no-op.
	w2 := doTCXDetach(t, h, workoutID)
	if w2.Code != http.StatusNoContent {
		t.Errorf("second detach status = %d, want 204", w2.Code)
	}
}

func TestWorkoutList_EnrichmentOmitsTrackpoints(t *testing.T) {
	h, _, _ := newTCXHandler(t)
	if imp := doTCXImport(t, h, strengthFixture(t, "strength_session.tcx")); imp.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201", imp.Code)
	}

	req := httptest.NewRequest("GET", "/workouts", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), tcxTestUser))
	rec := httptest.NewRecorder()
	h.list(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Items []struct {
				Enrichment *enrichmentEnvelope `json:"enrichment"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(env.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(env.Data.Items))
	}
	e := env.Data.Items[0].Enrichment
	if e == nil {
		t.Fatal("list enrichment = nil, want summary present")
	}
	if e.AvgHeartRateBpm == nil || *e.AvgHeartRateBpm != 140 {
		t.Errorf("list avg_heart_rate_bpm = %v, want 140", e.AvgHeartRateBpm)
	}
	if len(e.Trackpoints) != 0 {
		t.Errorf("list enrichment.trackpoints = %d, want 0 (omitted on list)", len(e.Trackpoints))
	}
}

func TestImportFromTCX_ValidationSlugs(t *testing.T) {
	h, _, _ := newTCXHandler(t)

	cases := []struct {
		name     string
		fixture  string
		wantCode int
		wantSlug string
	}{
		{"malformed", "malformed.tcx", http.StatusBadRequest, "tcx_parse_failed"},
		{"no effort data", "no_effort_data.tcx", http.StatusBadRequest, "tcx_no_effort_data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doTCXImport(t, h, strengthFixture(t, tc.fixture))
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.wantCode, w.Body.String())
			}
			var env struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.Code != tc.wantSlug {
				t.Errorf("code = %q, want %q", env.Code, tc.wantSlug)
			}
		})
	}
}

func TestImportFromTCX_FileTooLarge(t *testing.T) {
	h, _, _ := newTCXHandler(t)
	big := make([]byte, (10<<20)+1024) // just over the 10 MB cap
	w := doTCXImport(t, h, big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "file_too_large" {
		t.Errorf("code = %q, want file_too_large", env.Code)
	}
}
