package strength

// Coverage for the unified /activities strength surface: its `details` payload
// must carry personal_records_set field-for-field with the non-null-empty-slice
// semantics web relies on, and the imports + one-RM-history endpoints must
// answer under /activities with the expected shapes. Written during stage 3 as
// parity-against-/workouts; the /workouts shims were removed in stage 5, so
// these now assert the unified surface directly.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// legacyPRFields is the set of keys the personal_records_set event objects have
// always carried; web maps them 1:1, so a renamed or dropped field is a break.
var legacyPRFields = []string{
	"id", "exercise_id", "workout_id", "weight", "reps", "unit",
	"previous_weight", "previous_reps", "previous_unit", "achieved_at",
}

// seedWorkoutWeighted seeds a one-exercise back-squat workout at the given
// weight, so tests can control whether it breaks the standing PR.
func seedWorkoutWeighted(t *testing.T, repo *SQLiteRepository, start time.Time, weight float64) *Workout {
	t.Helper()
	w := &Workout{
		UserID:      "u1",
		Name:        "Light day",
		PerformedAt: start,
		Exercises: []WorkoutExercise{{
			ExerciseID: "back-squat",
			Order:      0,
			Sets:       []Set{{Reps: 5, Weight: weight, Unit: "lb"}},
		}},
	}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("create workout: %v", err)
	}
	return w
}

// prSetFromJSON normalizes a personal_records_set payload for deep
// comparison: unmarshal into generic maps so field names and values compare
// exactly as serialized (a renamed or dropped field fails the test). Fatals if
// the key is absent or serialized as null (the DTO never emits null).
func prSetFromJSON(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("personal_records_set key absent")
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode personal_records_set: %v; raw=%s", err, string(raw))
	}
	if out == nil {
		t.Fatalf("personal_records_set is null, want [] (DTO never emits null)")
	}
	return out
}

// unifiedStrengthDetails decodes the strength `details` object off a unified
// item.
type unifiedStrengthDetails struct {
	Exercises          json.RawMessage `json:"exercises"`
	PersonalRecordsSet json.RawMessage `json:"personal_records_set"`
}

func decodeUnifiedDetails(t *testing.T, details json.RawMessage) unifiedStrengthDetails {
	t.Helper()
	if len(details) == 0 {
		t.Fatalf("unified item has no details payload")
	}
	var d unifiedStrengthDetails
	if err := json.Unmarshal(details, &d); err != nil {
		t.Fatalf("decode unified details: %v; raw=%s", err, string(details))
	}
	return d
}

// TestUnifiedList_PersonalRecords: GET /activities?type=strength_training embeds
// each lift's exercises AND personal_records_set inside details. A PR-setting
// workout carries its events (with every legacy field); a lighter workout that
// sets none carries the present-but-empty [] (not null).
func TestUnifiedList_PersonalRecords(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)

	// First workout sets a PR (fresh DB); a second, lighter one doesn't.
	prWorkout := seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	noPR := seedWorkoutWeighted(t, repo, time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC), 135)

	w := doReq(t, srv, "GET", "/activities?type=strength_training", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /activities = %d; body=%s", w.Code, w.Body.String())
	}
	var unifiedEnv struct {
		Data struct {
			Activities []unifiedItem `json:"activities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &unifiedEnv); err != nil {
		t.Fatalf("decode unified list: %v", err)
	}
	if len(unifiedEnv.Data.Activities) != 2 {
		t.Fatalf("unified list has %d items, want 2", len(unifiedEnv.Data.Activities))
	}
	byID := make(map[string]unifiedItem, 2)
	for _, it := range unifiedEnv.Data.Activities {
		byID[it.ID] = it
	}

	// PR workout: events present, each carrying every legacy field, and its
	// exercises embedded in the list details.
	prDetails := decodeUnifiedDetails(t, byID[prWorkout.ID].Details)
	prs := prSetFromJSON(t, prDetails.PersonalRecordsSet)
	if len(prs) == 0 {
		t.Fatalf("PR workout details missing PR events")
	}
	for _, key := range legacyPRFields {
		if _, ok := prs[0][key]; !ok {
			t.Errorf("PR event missing legacy field %q: %v", key, prs[0])
		}
	}
	var gotEx []map[string]any
	if err := json.Unmarshal(prDetails.Exercises, &gotEx); err != nil {
		t.Fatalf("decode unified exercises: %v", err)
	}
	if len(gotEx) == 0 {
		t.Errorf("exercises not embedded in list details")
	}

	// No-PR workout: present-but-empty [] (prSetFromJSON fatals on null).
	noPRDetails := decodeUnifiedDetails(t, byID[noPR.ID].Details)
	if got := prSetFromJSON(t, noPRDetails.PersonalRecordsSet); len(got) != 0 {
		t.Errorf("no-PR workout personal_records_set = %v, want []", got)
	}
}

// TestUnifiedGet_PersonalRecords: the unified detail read embeds the lift's
// personal_records_set.
func TestUnifiedGet_PersonalRecords(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)
	wkt := seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))

	w := doReq(t, srv, "GET", "/activities/"+wkt.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /activities/{id} = %d; body=%s", w.Code, w.Body.String())
	}
	item := decodeItem(t, w)
	d := decodeUnifiedDetails(t, item.Details)
	prs := prSetFromJSON(t, d.PersonalRecordsSet)
	if len(prs) == 0 {
		t.Fatalf("detail personal_records_set is empty, want the seeded PR events")
	}
	for _, key := range legacyPRFields {
		if _, ok := prs[0][key]; !ok {
			t.Errorf("PR event missing legacy field %q: %v", key, prs[0])
		}
	}
}

// TestUnifiedCreate_ResponseEmbedsPersonalRecords: the POST /activities
// response (a fresh PR-setting lift) carries the events inline, so web can
// badge without a follow-up read.
func TestUnifiedCreate_ResponseEmbedsPersonalRecords(t *testing.T) {
	srv, _, _ := newUnifiedStack(t)

	w := doReq(t, srv, "POST", "/activities", `{
		"activity_type":"strength_training",
		"start_time":"2026-07-01T10:00:00Z",
		"name":"Push day",
		"details":{"exercises":[{"exercise_id":"barbell-bench-press","sets":[{"reps":5,"weight":185,"unit":"lb"}]}]}
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}
	item := decodeItem(t, w)
	d := decodeUnifiedDetails(t, item.Details)
	prs := prSetFromJSON(t, d.PersonalRecordsSet)
	if len(prs) != 1 {
		t.Fatalf("created lift personal_records_set = %v, want the fresh bench PR", prs)
	}
	for _, key := range legacyPRFields {
		if _, ok := prs[0][key]; !ok {
			t.Errorf("PR event missing legacy field %q: %v", key, prs[0])
		}
	}
}

// TestActivitiesImports: POST /activities/imports creates a strength session
// from a TCX, and re-posting the same file dedups (409) — proving one handler +
// dedup space behind the unified path.
func TestActivitiesImports(t *testing.T) {
	srv, _, _ := newUnifiedStack(t)

	body, ct := tcxMultipart(t, strengthFixture(t, "strength_session.tcx"))
	req := httptest.NewRequest("POST", "/activities/imports", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /activities/imports = %d; body=%s", w.Code, w.Body.String())
	}

	var env tcxWorkoutEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if env.Data.ID == "" {
		t.Fatal("imported workout has no id")
	}
	if env.Data.ActivityID == nil || *env.Data.ActivityID != env.Data.ID {
		t.Errorf("activity_id = %v, want the workout's own id (TCX attached)", env.Data.ActivityID)
	}
	if env.Data.Enrichment == nil {
		t.Fatal("imported workout has no enrichment block")
	}
	if len(env.Data.Exercises) != 0 {
		t.Errorf("imported workout has %d exercises, want 0 (empty session to fill in)", len(env.Data.Exercises))
	}

	// The same file re-imported is a duplicate — proving the dedup space.
	body, ct = tcxMultipart(t, strengthFixture(t, "strength_session.tcx"))
	req = httptest.NewRequest("POST", "/activities/imports", body)
	req.Header.Set("Content-Type", ct)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("re-import via /activities/imports = %d, want 409", w.Code)
	}
}

// TestActivitiesOneRMHistoryAlias: the per-exercise 1RM history answers both at
// its canonical /personal-records path and the /activities alias with a
// byte-identical payload (same handler).
func TestActivitiesOneRMHistoryAlias(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)
	seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))

	canonical := doReq(t, srv, "GET", "/personal-records/back-squat/history", "")
	if canonical.Code != http.StatusOK {
		t.Fatalf("GET /personal-records/{id}/history = %d; body=%s", canonical.Code, canonical.Body.String())
	}
	alias := doReq(t, srv, "GET", "/activities/personal-records/back-squat/history", "")
	if alias.Code != http.StatusOK {
		t.Fatalf("GET /activities/personal-records/{id}/history = %d; body=%s", alias.Code, alias.Body.String())
	}
	if canonical.Body.String() != alias.Body.String() {
		t.Errorf("alias body differs:\nalias=%s\ncanonical=%s", alias.Body.String(), canonical.Body.String())
	}
}
