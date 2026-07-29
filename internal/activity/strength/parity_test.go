package strength

// Parity tests for the unified /activities surface vs the legacy /workouts
// shims (stage-3 SOW: web migrates off /workouts). The unified strength
// `details` payload must carry the SAME personal_records_set the legacy DTOs
// embed — field-for-field, non-null-empty-slice semantics included — and the
// imports + one-RM-history endpoints must answer under /activities with the
// legacy shapes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

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
// exactly as serialized (a renamed or dropped field fails the test).
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
		t.Fatalf("personal_records_set is null, want [] (legacy DTO never emits null)")
	}
	return out
}

// legacyWorkoutItem decodes the fields of the /workouts DTO these parity
// tests compare against.
type legacyWorkoutItem struct {
	ID                 string          `json:"id"`
	PersonalRecordsSet json.RawMessage `json:"personal_records_set"`
	Exercises          json.RawMessage `json:"exercises"`
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

// TestUnifiedList_PersonalRecordsParity: GET /activities?type=strength_training
// embeds each lift's exercises AND personal_records_set inside details,
// matching GET /workouts item-for-item and field-for-field. The second
// seeded workout sets no PRs, so it proves the present-but-empty ([] not
// null) contract too.
func TestUnifiedList_PersonalRecordsParity(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)

	// First workout sets a PR (fresh DB); a second, lighter one doesn't.
	prWorkout := seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	noPR := seedWorkoutWeighted(t, repo, time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC), 135)

	w := doReq(t, srv, "GET", "/workouts", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /workouts = %d; body=%s", w.Code, w.Body.String())
	}
	var legacyEnv struct {
		Data struct {
			Items []legacyWorkoutItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &legacyEnv); err != nil {
		t.Fatalf("decode legacy list: %v", err)
	}
	legacyByID := make(map[string]legacyWorkoutItem)
	for _, it := range legacyEnv.Data.Items {
		legacyByID[it.ID] = it
	}
	if len(legacyByID) != 2 {
		t.Fatalf("legacy list has %d items, want 2", len(legacyByID))
	}

	w = doReq(t, srv, "GET", "/activities?type=strength_training", "")
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

	for _, item := range unifiedEnv.Data.Activities {
		legacy, ok := legacyByID[item.ID]
		if !ok {
			t.Fatalf("unified item %s not in legacy list", item.ID)
		}
		d := decodeUnifiedDetails(t, item.Details)

		gotPRs := prSetFromJSON(t, d.PersonalRecordsSet)
		wantPRs := prSetFromJSON(t, legacy.PersonalRecordsSet)
		if !reflect.DeepEqual(gotPRs, wantPRs) {
			t.Errorf("item %s personal_records_set mismatch:\nunified=%s\nlegacy=%s",
				item.ID, string(d.PersonalRecordsSet), string(legacy.PersonalRecordsSet))
		}

		// Exercises ride along in the list details (the legacy list embeds
		// full exercises per item; web's workouts view renders them).
		var gotEx, wantEx []map[string]any
		if err := json.Unmarshal(d.Exercises, &gotEx); err != nil {
			t.Fatalf("decode unified exercises: %v", err)
		}
		if err := json.Unmarshal(legacy.Exercises, &wantEx); err != nil {
			t.Fatalf("decode legacy exercises: %v", err)
		}
		if !reflect.DeepEqual(gotEx, wantEx) {
			t.Errorf("item %s exercises mismatch:\nunified=%s\nlegacy=%s",
				item.ID, string(d.Exercises), string(legacy.Exercises))
		}
	}

	// Sanity: the PR workout really has events and the lighter one has [].
	prLegacy := prSetFromJSON(t, legacyByID[prWorkout.ID].PersonalRecordsSet)
	if len(prLegacy) == 0 {
		t.Fatalf("seeded PR workout has no PR events — fixture assumption broken")
	}
	if got := prSetFromJSON(t, legacyByID[noPR.ID].PersonalRecordsSet); len(got) != 0 {
		t.Fatalf("no-PR workout has events %v — fixture assumption broken", got)
	}
}

// TestUnifiedGet_PersonalRecordsParity: the unified detail read embeds the
// same personal_records_set as GET /workouts/{id}.
func TestUnifiedGet_PersonalRecordsParity(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)
	wkt := seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))

	w := doReq(t, srv, "GET", "/workouts/"+wkt.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /workouts/{id} = %d; body=%s", w.Code, w.Body.String())
	}
	var legacyEnv struct {
		Data legacyWorkoutItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &legacyEnv); err != nil {
		t.Fatalf("decode legacy detail: %v", err)
	}
	wantPRs := prSetFromJSON(t, legacyEnv.Data.PersonalRecordsSet)
	if len(wantPRs) == 0 {
		t.Fatalf("seeded workout set no PRs — fixture assumption broken")
	}

	w = doReq(t, srv, "GET", "/activities/"+wkt.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /activities/{id} = %d; body=%s", w.Code, w.Body.String())
	}
	item := decodeItem(t, w)
	d := decodeUnifiedDetails(t, item.Details)
	gotPRs := prSetFromJSON(t, d.PersonalRecordsSet)
	if !reflect.DeepEqual(gotPRs, wantPRs) {
		t.Errorf("detail personal_records_set mismatch:\nunified=%s\nlegacy=%s",
			string(d.PersonalRecordsSet), string(legacyEnv.Data.PersonalRecordsSet))
	}
}

// TestUnifiedCreate_ResponseEmbedsPersonalRecords: the POST /activities
// response (a fresh PR-setting lift) carries the events inline, so web can
// badge without a follow-up read — parity with POST /workouts returning the
// created workout (whose subsequent GET carried the events).
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
	for _, key := range []string{"id", "exercise_id", "workout_id", "weight", "reps", "unit", "previous_weight", "previous_reps", "previous_unit", "achieved_at"} {
		if _, ok := prs[0][key]; !ok {
			t.Errorf("PR event missing legacy field %q: %v", key, prs[0])
		}
	}
}

// TestActivitiesImportsAlias: POST /activities/imports is the same
// create-strength-session-from-TCX endpoint as POST /workouts/imports —
// same handler, same response shape.
func TestActivitiesImportsAlias(t *testing.T) {
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

	// The same file at the legacy alias is now a duplicate — proving both
	// paths share one handler + dedup space.
	body, ct = tcxMultipart(t, strengthFixture(t, "strength_session.tcx"))
	req = httptest.NewRequest("POST", "/workouts/imports", body)
	req.Header.Set("Content-Type", ct)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("re-import via /workouts/imports = %d, want 409", w.Code)
	}
}

// TestActivitiesOneRMHistoryAlias: the per-exercise 1RM history relocates
// under /activities with a byte-identical payload (same handler).
func TestActivitiesOneRMHistoryAlias(t *testing.T) {
	srv, repo, _ := newUnifiedStack(t)
	seedWorkout(t, repo, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))

	legacy := doReq(t, srv, "GET", "/personal-records/back-squat/history", "")
	if legacy.Code != http.StatusOK {
		t.Fatalf("GET /personal-records/{id}/history = %d; body=%s", legacy.Code, legacy.Body.String())
	}
	alias := doReq(t, srv, "GET", "/activities/personal-records/back-squat/history", "")
	if alias.Code != http.StatusOK {
		t.Fatalf("GET /activities/personal-records/{id}/history = %d; body=%s", alias.Code, alias.Body.String())
	}
	if legacy.Body.String() != alias.Body.String() {
		t.Errorf("alias body differs:\nalias=%s\nlegacy=%s", alias.Body.String(), legacy.Body.String())
	}
}
