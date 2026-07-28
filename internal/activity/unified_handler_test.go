package activity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// newUnifiedServer builds the activity handler with the four endurance
// descriptors registered and mounted on a real chi router behind a fake auth
// middleware, mirroring the server wiring. The strength descriptor lives in
// the child package this one must not import, so the strength-involving
// unified-surface tests live in internal/activity/strength.
func newUnifiedServer(t *testing.T) (http.Handler, *SQLiteRepository) {
	t.Helper()
	d := dbtest.New(t)
	repo := NewSQLiteRepository(d, NewMemoryArchiver())
	h := NewHandler(repo)
	h.SetRegistry(NewRegistry(
		NewEnduranceDescriptor(ActivityRunning, NewSQLiteEnduranceDetailStore(d, ActivityRunning)),
		NewEnduranceDescriptor(ActivityWalking, NewSQLiteEnduranceDetailStore(d, ActivityWalking)),
		NewEnduranceDescriptor(ActivityCycling, NewSQLiteEnduranceDetailStore(d, ActivityCycling)),
		NewEnduranceDescriptor(ActivityOther, NewSQLiteEnduranceDetailStore(d, ActivityOther)),
	))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), testUserID)))
		})
	})
	h.Mount(r)
	return r, repo
}

func doJSON(t *testing.T, srv http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func decodeActivity(t *testing.T, w *httptest.ResponseRecorder) activityDTO {
	t.Helper()
	var env activityEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode activity envelope: %v; body=%s", err, w.Body.String())
	}
	return env.Data
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder) listResponse {
	t.Helper()
	var env listEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode list envelope: %v; body=%s", err, w.Body.String())
	}
	return env.Data
}

// TestUnifiedList_TypeFilterAndSummaries covers scenario (b): the list carries
// activity_type + summary per item and ?type= filters to one registered type.
func TestUnifiedList_TypeFilterAndSummaries(t *testing.T) {
	srv, repo := newUnifiedServer(t)
	ctx := t.Context()

	run := newActivity("u1", IngestManualTCX, "run1", mustTime(t, "2026-07-01T07:00:00Z"), 8046.7, 2472)
	if err := repo.Create(ctx, run, []byte("run")); err != nil {
		t.Fatalf("create run: %v", err)
	}
	walk := newActivity("u1", IngestManualTCX, "walk1", mustTime(t, "2026-07-02T07:00:00Z"), 3000, 1800)
	walk.ActivityType = ActivityWalking
	if err := repo.Create(ctx, walk, []byte("walk")); err != nil {
		t.Fatalf("create walk: %v", err)
	}

	w := doJSON(t, srv, "GET", "/activities", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", w.Code, w.Body.String())
	}
	all := decodeList(t, w)
	if len(all.Activities) != 2 {
		t.Fatalf("list = %d items, want 2", len(all.Activities))
	}
	for _, a := range all.Activities {
		if a.ActivityType == "" {
			t.Errorf("item %s missing activity_type", a.ID)
		}
		if a.Summary == nil {
			t.Errorf("item %s missing summary", a.ID)
		}
	}
	// The run card renders the hydrator's "5.0 mi · 41:12" vocabulary.
	for _, a := range all.Activities {
		if a.ActivityType == ActivityRunning && a.Summary != nil {
			if a.Summary.Subtitle != "5.0 mi · 41:12" {
				t.Errorf("run summary subtitle = %q, want %q", a.Summary.Subtitle, "5.0 mi · 41:12")
			}
		}
	}

	w = doJSON(t, srv, "GET", "/activities?type=running", "")
	if w.Code != http.StatusOK {
		t.Fatalf("filtered list status = %d; body=%s", w.Code, w.Body.String())
	}
	filtered := decodeList(t, w)
	if len(filtered.Activities) != 1 || filtered.Activities[0].ActivityType != ActivityRunning {
		t.Fatalf("?type=running = %+v, want exactly the run", filtered.Activities)
	}

	w = doJSON(t, srv, "GET", "/activities?type=zumba", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("?type=zumba status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// TestUnifiedCreate_UnknownType covers scenario (e): an unregistered type is a
// 422 naming the valid set.
func TestUnifiedCreate_UnknownType(t *testing.T) {
	srv, _ := newUnifiedServer(t)
	w := doJSON(t, srv, "POST", "/activities",
		`{"activity_type":"kickboxing","start_time":"2026-07-01T10:00:00Z"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "running") {
		t.Errorf("422 body should list the valid types; body=%s", w.Body.String())
	}
}

// TestUnifiedCreate_MissingStartTime: base-field validation is the handler's.
func TestUnifiedCreate_MissingStartTime(t *testing.T) {
	srv, _ := newUnifiedServer(t)
	w := doJSON(t, srv, "POST", "/activities", `{"activity_type":"other"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// TestUnifiedCreate_RunRequiresDistance: the endurance descriptor's
// ValidateCreate gates the write (400, not a half-written row).
func TestUnifiedCreate_RunRequiresDistance(t *testing.T) {
	srv, repo := newUnifiedServer(t)
	w := doJSON(t, srv, "POST", "/activities",
		`{"activity_type":"running","start_time":"2026-07-01T10:00:00Z","duration_seconds":1800}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	got, err := repo.List(t.Context(), testUserID, 10, nil, TypeFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("rejected create persisted %d rows, want 0", len(got))
	}
}

// TestUnifiedRun_CreatePutDeleteRoundTrip covers the run half of scenario (f).
func TestUnifiedRun_CreatePutDeleteRoundTrip(t *testing.T) {
	srv, _ := newUnifiedServer(t)

	w := doJSON(t, srv, "POST", "/activities",
		`{"activity_type":"running","start_time":"2026-07-01T10:00:00Z","duration_seconds":2472,"name":"Tempo",
		  "details":{"distance_meters":8046.7}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}
	created := decodeActivity(t, w)
	if created.ID == "" {
		t.Fatal("created activity has no id")
	}
	if created.ActivityType != ActivityRunning {
		t.Errorf("activity_type = %q, want running", created.ActivityType)
	}

	// Assert via GET: base fields + typed details from the detail table.
	w = doJSON(t, srv, "GET", "/activities/"+created.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", w.Code, w.Body.String())
	}
	got := decodeActivity(t, w)
	if got.Name == nil || *got.Name != "Tempo" {
		t.Errorf("name = %v, want Tempo", got.Name)
	}
	if got.DurationSeconds != 2472 {
		t.Errorf("duration = %d, want 2472", got.DurationSeconds)
	}
	details, ok := got.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T (%v), want an object", got.Details, got.Details)
	}
	if dm, _ := details["distance_meters"].(float64); dm != 8046.7 {
		t.Errorf("details.distance_meters = %v, want 8046.7", details["distance_meters"])
	}

	// PUT: full replacement of base + details.
	w = doJSON(t, srv, "PUT", "/activities/"+created.ID,
		`{"activity_type":"running","start_time":"2026-07-01T10:00:00Z","duration_seconds":2400,"name":"Tempo v2",
		  "details":{"distance_meters":9000}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d; body=%s", w.Code, w.Body.String())
	}
	w = doJSON(t, srv, "GET", "/activities/"+created.ID, "")
	got = decodeActivity(t, w)
	if got.Name == nil || *got.Name != "Tempo v2" {
		t.Errorf("post-PUT name = %v, want Tempo v2", got.Name)
	}
	if got.DurationSeconds != 2400 {
		t.Errorf("post-PUT duration = %d, want 2400", got.DurationSeconds)
	}
	details, _ = got.Details.(map[string]any)
	if dm, _ := details["distance_meters"].(float64); dm != 9000 {
		t.Errorf("post-PUT details.distance_meters = %v, want 9000", details["distance_meters"])
	}

	// Changing the type on PUT is rejected.
	w = doJSON(t, srv, "PUT", "/activities/"+created.ID,
		`{"activity_type":"walking","start_time":"2026-07-01T10:00:00Z","details":{"distance_meters":9000}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("type-change put status = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	// DELETE stays soft and reads as gone.
	w = doJSON(t, srv, "DELETE", "/activities/"+created.ID, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d; body=%s", w.Code, w.Body.String())
	}
	w = doJSON(t, srv, "GET", "/activities/"+created.ID, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", w.Code)
	}
}

// TestUnifiedPut_PreservesVitalsWhenAbsent: a PUT that omits the vitals
// keys must not strip a session's TCX-derived HR/calories (the trackpoints
// would remain — inconsistent state); explicit values still replace them.
func TestUnifiedPut_PreservesVitalsWhenAbsent(t *testing.T) {
	srv, repo := newUnifiedServer(t)
	ctx := t.Context()

	run := newActivity("u1", IngestManualTCX, "run-vitals", mustTime(t, "2026-07-01T07:00:00Z"), 8046.7, 2472)
	run.AvgHeartRateBpm = ptrInt(152)
	run.MaxHeartRateBpm = ptrInt(181)
	run.TotalCalories = ptrInt(610)
	if err := repo.Create(ctx, run, []byte("run")); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Rename-style PUT without vitals keys: vitals survive.
	w := doJSON(t, srv, "PUT", "/activities/"+run.ID,
		`{"activity_type":"running","start_time":"2026-07-01T07:00:00Z","duration_seconds":2472,"name":"Renamed",
		  "details":{"distance_meters":8046.7}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d; body=%s", w.Code, w.Body.String())
	}
	w = doJSON(t, srv, "GET", "/activities/"+run.ID, "")
	got := decodeActivity(t, w)
	if got.Name == nil || *got.Name != "Renamed" {
		t.Errorf("name = %v, want Renamed", got.Name)
	}
	if got.AvgHeartRateBpm == nil || *got.AvgHeartRateBpm != 152 {
		t.Errorf("avg hr = %v, want preserved 152", got.AvgHeartRateBpm)
	}
	if got.MaxHeartRateBpm == nil || *got.MaxHeartRateBpm != 181 {
		t.Errorf("max hr = %v, want preserved 181", got.MaxHeartRateBpm)
	}
	if got.TotalCalories == nil || *got.TotalCalories != 610 {
		t.Errorf("calories = %v, want preserved 610", got.TotalCalories)
	}

	// Explicit vitals replace.
	w = doJSON(t, srv, "PUT", "/activities/"+run.ID,
		`{"activity_type":"running","start_time":"2026-07-01T07:00:00Z","duration_seconds":2472,"name":"Renamed",
		  "avg_heart_rate_bpm":140,"max_heart_rate_bpm":170,"total_calories":500,
		  "details":{"distance_meters":8046.7}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put with vitals status = %d; body=%s", w.Code, w.Body.String())
	}
	w = doJSON(t, srv, "GET", "/activities/"+run.ID, "")
	got = decodeActivity(t, w)
	if got.AvgHeartRateBpm == nil || *got.AvgHeartRateBpm != 140 {
		t.Errorf("avg hr = %v, want replaced 140", got.AvgHeartRateBpm)
	}
	if got.MaxHeartRateBpm == nil || *got.MaxHeartRateBpm != 170 {
		t.Errorf("max hr = %v, want replaced 170", got.MaxHeartRateBpm)
	}
	if got.TotalCalories == nil || *got.TotalCalories != 500 {
		t.Errorf("calories = %v, want replaced 500", got.TotalCalories)
	}
}

// TestUnifiedCreate_BaseOnlyOther covers scenario (g): a base-only shaped
// manual log (the future-kickboxing path) — type `other`, no details — is a
// legitimate create, appears in the list with a summary, and its GET omits
// the details block.
func TestUnifiedCreate_BaseOnlyOther(t *testing.T) {
	srv, _ := newUnifiedServer(t)

	w := doJSON(t, srv, "POST", "/activities",
		`{"activity_type":"other","start_time":"2026-07-01T18:00:00Z","duration_seconds":3600,"name":"Kickboxing"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}
	created := decodeActivity(t, w)

	w = doJSON(t, srv, "GET", "/activities/"+created.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", w.Code, w.Body.String())
	}
	got := decodeActivity(t, w)
	if got.Details != nil {
		t.Errorf("details = %v, want omitted for a base-only create", got.Details)
	}
	if got.Name == nil || *got.Name != "Kickboxing" {
		t.Errorf("name = %v, want Kickboxing", got.Name)
	}
	if got.DurationSeconds != 3600 {
		t.Errorf("duration = %d, want 3600", got.DurationSeconds)
	}

	w = doJSON(t, srv, "GET", "/activities", "")
	listed := decodeList(t, w)
	if len(listed.Activities) != 1 {
		t.Fatalf("list = %d items, want 1", len(listed.Activities))
	}
	item := listed.Activities[0]
	if item.Summary == nil || item.Summary.Title != "Kickboxing" {
		t.Errorf("summary = %+v, want title Kickboxing", item.Summary)
	}
}

// TestUnifiedCreate_NotesRoundTrip: notes are a base column the unified
// surface reads and writes.
func TestUnifiedCreate_NotesRoundTrip(t *testing.T) {
	srv, _ := newUnifiedServer(t)
	w := doJSON(t, srv, "POST", "/activities",
		`{"activity_type":"other","start_time":"2026-07-01T18:00:00Z","notes":"felt great"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", w.Code, w.Body.String())
	}
	created := decodeActivity(t, w)
	w = doJSON(t, srv, "GET", "/activities/"+created.ID, "")
	got := decodeActivity(t, w)
	if got.Notes == nil || *got.Notes != "felt great" {
		t.Errorf("notes = %v, want 'felt great'", got.Notes)
	}
}
