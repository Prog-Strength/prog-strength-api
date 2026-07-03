package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/hrzones"
)

const testUserID = "u1"

// --- envelopes for assertions --------------------------------------

type activityEnvelope struct {
	Message string      `json:"message"`
	Data    activityDTO `json:"data"`
}

type listEnvelope struct {
	Message string       `json:"message"`
	Data    listResponse `json:"data"`
}

type metricsEnvelope struct {
	Message string          `json:"message"`
	Data    metricsResponse `json:"data"`
}

type codeEnvelope struct {
	Error              string `json:"error"`
	Code               string `json:"code"`
	ExistingActivityID string `json:"existing_activity_id"`
}

// --- helpers -------------------------------------------------------

func newTestHandler(t *testing.T) (*Handler, *MemoryArchiver, *SQLiteRepository) {
	// The in-memory ARCHIVER (object storage no-S3 fallback) stays; only the
	// in-memory REPOSITORY moves to ephemeral SQLite.
	arch := NewMemoryArchiver()
	repo := NewSQLiteRepository(dbtest.New(t), arch)
	return NewHandler(repo), arch, repo
}

// testHRZonesEngine builds an engine mirroring the [hr_zones] config defaults
// so handler tests exercise the same zone model the server wires up.
func testHRZonesEngine() *hrzones.Engine {
	return hrzones.New(hrzones.Config{
		PopulationDefaultMaxHR: 190,
		CalibratedRunThreshold: 5,
		RecencyWindowDays:      90,
		MinReferenceBpm:        100,
		MaxReferenceBpm:        230,
		ZoneUpperBounds:        []float64{0.60, 0.70, 0.80, 0.90},
		ZoneNames:              []string{"Recovery", "Aerobic", "Tempo", "Threshold", "VO2max"},
	})
}

// multipartBody builds a multipart/form-data body with the TCX bytes
// under the "file" field and returns the body plus its Content-Type
// header.
func multipartBody(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "activity.tcx")
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

// doImport drives uploadTCX with a multipart upload of data.
func doImport(t *testing.T, h *Handler, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartBody(t, data)
	req := httptest.NewRequest("POST", "/activities/tcx", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.uploadTCX(w, req)
	return w
}

// withParam attaches a chi URL param to the request context.
func withParam(req *http.Request, key, val string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add(key, val)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

// --- import happy path + duplicate ---------------------------------

func TestImportHappyPath(t *testing.T) {
	h, arch, _ := newTestHandler(t)
	w := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var env activityEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ID == "" {
		t.Error("expected non-empty activity id")
	}
	if env.Data.SourceActivityID != "2026-01-02T08:00:00Z" {
		t.Errorf("source_activity_id = %q", env.Data.SourceActivityID)
	}
	if env.Data.ActivityType != ActivityRunning {
		t.Errorf("activity_type = %q, want %q", env.Data.ActivityType, ActivityRunning)
	}
	if env.Data.IngestSource != IngestManualTCX {
		t.Errorf("ingest_source = %q, want %q", env.Data.IngestSource, IngestManualTCX)
	}
	if env.Data.DistanceMeters != 5000 {
		t.Errorf("distance = %v, want 5000", env.Data.DistanceMeters)
	}
	if len(env.Data.Trackpoints) == 0 {
		t.Error("expected trackpoints on import response")
	}
	if arch.Len() != 1 {
		t.Errorf("archiver = %d objects, want 1", arch.Len())
	}
}

// TestImportEnvironmentFields asserts the new DTO fields flow through ingest:
// the outdoor 5k fixture returns environment=outdoor with raw==distance, while
// the no-position treadmill fixture returns environment=indoor and writes zero
// activity_best_efforts rows (indoor runs are excluded from PR surfaces).
func TestImportEnvironmentFields(t *testing.T) {
	h, _, repo := newTestHandler(t)

	out := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if out.Code != http.StatusCreated {
		t.Fatalf("outdoor import status = %d; body=%s", out.Code, out.Body.String())
	}
	var outEnv activityEnvelope
	if err := json.Unmarshal(out.Body.Bytes(), &outEnv); err != nil {
		t.Fatalf("decode outdoor: %v", err)
	}
	if outEnv.Data.Environment != EnvironmentOutdoor {
		t.Errorf("outdoor environment = %q, want outdoor", outEnv.Data.Environment)
	}
	if outEnv.Data.RawDistanceMeters != outEnv.Data.DistanceMeters {
		t.Errorf("outdoor raw_distance = %.2f, want == distance %.2f", outEnv.Data.RawDistanceMeters, outEnv.Data.DistanceMeters)
	}

	in := doImport(t, h, readFixture(t, "treadmill_5k.tcx"))
	if in.Code != http.StatusCreated {
		t.Fatalf("treadmill import status = %d; body=%s", in.Code, in.Body.String())
	}
	var inEnv activityEnvelope
	if err := json.Unmarshal(in.Body.Bytes(), &inEnv); err != nil {
		t.Fatalf("decode treadmill: %v", err)
	}
	if inEnv.Data.Environment != EnvironmentIndoor {
		t.Errorf("treadmill environment = %q, want indoor", inEnv.Data.Environment)
	}
	if inEnv.Data.RawDistanceMeters != inEnv.Data.DistanceMeters {
		t.Errorf("treadmill raw_distance = %.2f, want == distance %.2f", inEnv.Data.RawDistanceMeters, inEnv.Data.DistanceMeters)
	}

	// The indoor run must have written no best-effort rows.
	var count int
	if err := repo.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM activity_best_efforts WHERE activity_id = ?`, inEnv.Data.ID).Scan(&count); err != nil {
		t.Fatalf("count indoor best efforts: %v", err)
	}
	if count != 0 {
		t.Errorf("indoor run wrote %d best-effort rows, want 0", count)
	}
}

// A biking TCX no longer rejects with SlugNotRunning — the validator
// accepts any sport; the ingest pipeline classifies it as cycling.
func TestImportBikingClassifiesAsCycling(t *testing.T) {
	h, _, _ := newTestHandler(t)
	w := doImport(t, h, readFixture(t, "biking.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var env activityEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ActivityType != ActivityCycling {
		t.Errorf("activity_type = %q, want %q", env.Data.ActivityType, ActivityCycling)
	}
	// Cycling activities don't carry pace fields.
	if env.Data.AvgPaceSecPerKm != nil {
		t.Errorf("avg_pace_sec_per_km = %v, want nil for cycling", *env.Data.AvgPaceSecPerKm)
	}
	if env.Data.BestPaceSecPerKm != nil {
		t.Errorf("best_pace_sec_per_km = %v, want nil for cycling", *env.Data.BestPaceSecPerKm)
	}
}

func TestImportDuplicate(t *testing.T) {
	h, _, _ := newTestHandler(t)
	first := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first import status = %d", first.Code)
	}
	var firstEnv activityEnvelope
	if err := json.Unmarshal(first.Body.Bytes(), &firstEnv); err != nil {
		t.Fatalf("decode first: %v", err)
	}

	second := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if second.Code != http.StatusConflict {
		t.Fatalf("second import status = %d, want 409; body=%s", second.Code, second.Body.String())
	}
	var dup codeEnvelope
	if err := json.Unmarshal(second.Body.Bytes(), &dup); err != nil {
		t.Fatalf("decode dup: %v", err)
	}
	if dup.Code != "duplicate_activity" {
		t.Errorf("code = %q, want duplicate_activity", dup.Code)
	}
	if dup.ExistingActivityID != firstEnv.Data.ID {
		t.Errorf("existing_activity_id = %q, want %q", dup.ExistingActivityID, firstEnv.Data.ID)
	}
}

// --- validation slugs ----------------------------------------------

func TestImportValidationSlugs(t *testing.T) {
	cases := []struct {
		fixture string
		code    string
	}{
		{"empty.tcx", SlugEmpty},
		{"malformed.tcx", SlugParseFailed},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			h, _, _ := newTestHandler(t)
			w := doImport(t, h, readFixture(t, tc.fixture))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			var env codeEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.Code != tc.code {
				t.Errorf("code = %q, want %q", env.Code, tc.code)
			}
		})
	}
}

// --- oversized + wrong media type ----------------------------------

func TestImportOversized(t *testing.T) {
	h, _, _ := newTestHandler(t)
	big := bytes.Repeat([]byte("a"), maxTCXBytes+1024)
	body, ct := multipartBody(t, big)
	req := httptest.NewRequest("POST", "/activities/tcx", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.uploadTCX(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "file_too_large" {
		t.Errorf("code = %q, want file_too_large", env.Code)
	}
}

func TestImportNonMultipart(t *testing.T) {
	h, _, _ := newTestHandler(t)
	req := httptest.NewRequest("POST", "/activities/tcx", strings.NewReader(`{"foo":"bar"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.uploadTCX(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "unsupported_media_type" {
		t.Errorf("code = %q, want unsupported_media_type", env.Code)
	}
}

// --- storage failure + rollback ------------------------------------

func TestImportStorageFailure(t *testing.T) {
	h, arch, repo := newTestHandler(t)
	arch.PutErr = context.DeadlineExceeded
	w := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "storage_failed" {
		t.Errorf("code = %q, want storage_failed", env.Code)
	}
	got, err := repo.List(context.Background(), testUserID, 10, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 activities after storage failure, got %d", len(got))
	}
}

// --- list pagination via before= -----------------------------------

func TestListPagination(t *testing.T) {
	h, _, repo := newTestHandler(t)
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		a := newActivity(testUserID, IngestManualTCX, "act-"+string(rune('a'+i)),
			base.AddDate(0, 0, i), 1000, 300)
		if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	req := httptest.NewRequest("GET", "/activities?limit=2", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.list(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("page1 status = %d; body=%s", w.Code, w.Body.String())
	}
	var p1 listEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &p1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(p1.Data.Activities) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(p1.Data.Activities))
	}
	if p1.Data.NextBefore == nil {
		t.Fatal("expected non-nil next_before on full page")
	}

	req2 := httptest.NewRequest("GET", "/activities?limit=2&before="+*p1.Data.NextBefore, nil)
	req2 = req2.WithContext(authctx.WithUserID(req2.Context(), testUserID))
	w2 := httptest.NewRecorder()
	h.list(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d; body=%s", w2.Code, w2.Body.String())
	}
	var p2 listEnvelope
	if err := json.Unmarshal(w2.Body.Bytes(), &p2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(p2.Data.Activities) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(p2.Data.Activities))
	}
	if p2.Data.NextBefore != nil {
		t.Errorf("expected nil next_before on last page, got %q", *p2.Data.NextBefore)
	}
}

// --- list date-range (since/until) ---------------------------------

func TestListSinceUntil(t *testing.T) {
	h, _, repo := newTestHandler(t)
	starts := []time.Time{
		time.Date(2026, 2, 27, 7, 0, 0, 0, time.UTC),  // before range
		time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC),   // inside (lower edge, inclusive)
		time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC),  // inside
		time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC), // inside (top of month)
		time.Date(2026, 4, 1, 6, 0, 0, 0, time.UTC),   // after range (upper edge, exclusive)
	}
	for i, st := range starts {
		a := newActivity(testUserID, IngestManualTCX, "range-"+string(rune('a'+i)),
			st, 1000, 300)
		if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	url := "/activities?since=2026-03-01T00:00:00Z&until=2026-04-01T00:00:00Z"
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.list(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var env listEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := len(env.Data.Activities), 3; got != want {
		t.Fatalf("range result count = %d, want %d (March-only)", got, want)
	}
	if env.Data.NextBefore != nil {
		t.Errorf("range query returned a next_before cursor; range results are complete")
	}

	mix := httptest.NewRequest("GET", url+"&before=2026-03-15T00:00:00Z", nil)
	mix = mix.WithContext(authctx.WithUserID(mix.Context(), testUserID))
	mw := httptest.NewRecorder()
	h.list(mw, mix)
	if mw.Code != http.StatusBadRequest {
		t.Errorf("mixed since+before status = %d, want 400", mw.Code)
	}
}

// --- rename --------------------------------------------------------

func TestRenameHandler(t *testing.T) {
	h, _, repo := newTestHandler(t)
	a := newActivity(testUserID, IngestManualTCX, "x", time.Now().UTC(), 1000, 300)
	if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/activities/"+a.ID, strings.NewReader(`{"name":"Tempo Run"}`))
	req = withParam(req.WithContext(authctx.WithUserID(req.Context(), testUserID)), "id", a.ID)
	w := httptest.NewRecorder()
	h.rename(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rename status = %d; body=%s", w.Code, w.Body.String())
	}
	var env activityEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Name == nil || *env.Data.Name != "Tempo Run" {
		t.Errorf("name = %v, want Tempo Run", env.Data.Name)
	}

	for _, body := range []string{`{"name":""}`, `{"name":"` + strings.Repeat("a", 201) + `"}`} {
		req := httptest.NewRequest("PATCH", "/activities/"+a.ID, strings.NewReader(body))
		req = withParam(req.WithContext(authctx.WithUserID(req.Context(), testUserID)), "id", a.ID)
		w := httptest.NewRecorder()
		h.rename(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("rename %q status = %d, want 400", body, w.Code)
		}
	}

	req404 := httptest.NewRequest("PATCH", "/activities/nope", strings.NewReader(`{"name":"x"}`))
	req404 = withParam(req404.WithContext(authctx.WithUserID(req404.Context(), testUserID)), "id", "nope")
	w404 := httptest.NewRecorder()
	h.rename(w404, req404)
	if w404.Code != http.StatusNotFound {
		t.Errorf("rename missing status = %d, want 404", w404.Code)
	}
}

// --- delete then get -----------------------------------------------

func TestDeleteThenGet(t *testing.T) {
	h, _, repo := newTestHandler(t)
	a := newActivity(testUserID, IngestManualTCX, "x", time.Now().UTC(), 1000, 300)
	if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	del := httptest.NewRequest("DELETE", "/activities/"+a.ID, nil)
	del = withParam(del.WithContext(authctx.WithUserID(del.Context(), testUserID)), "id", a.ID)
	wd := httptest.NewRecorder()
	h.delete(wd, del)
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", wd.Code)
	}

	get := httptest.NewRequest("GET", "/activities/"+a.ID, nil)
	get = withParam(get.WithContext(authctx.WithUserID(get.Context(), testUserID)), "id", a.ID)
	wg := httptest.NewRecorder()
	h.get(wg, get)
	if wg.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", wg.Code)
	}
}

// --- running metrics -----------------------------------------------

func TestRunningMetricsHandler(t *testing.T) {
	h, _, repo := newTestHandler(t)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		a := newActivity(testUserID, IngestManualTCX, "m-"+string(rune('a'+i)),
			now.AddDate(0, 0, -i), 3000, 900)
		if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	req := httptest.NewRequest("GET", "/activities/running-metrics?timezone=America/Denver", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("metrics status = %d; body=%s", w.Code, w.Body.String())
	}
	var env metricsEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.AllTime.RunCount != 2 {
		t.Errorf("all_time.run_count = %d, want 2", env.Data.AllTime.RunCount)
	}
	if env.Data.AllTime.DistanceMeters != 6000 {
		t.Errorf("all_time.distance = %v, want 6000", env.Data.AllTime.DistanceMeters)
	}
}

func TestRunningMetricsHandlerMissingTimezone(t *testing.T) {
	h, _, _ := newTestHandler(t)
	req := httptest.NewRequest("GET", "/activities/running-metrics", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// --- running best efforts ----------------------------------------------

type bestEffortsEnvelope struct {
	Message string              `json:"message"`
	Data    bestEffortsResponse `json:"data"`
}

type bestEffortHistoryEnvelope struct {
	Message string                    `json:"message"`
	Data    bestEffortHistoryResponse `json:"data"`
}

// seedRunWithEfforts inserts a running activity carrying the given best
// efforts directly through the repo's Create path.
func seedRunWithEfforts(t *testing.T, repo *SQLiteRepository, source string, start time.Time, efforts []ActivityBestEffort) *Activity {
	t.Helper()
	avg := 300.0
	a := &Activity{
		UserID:           testUserID,
		ActivityType:     ActivityRunning,
		IngestSource:     IngestManualTCX,
		SourceActivityID: source,
		StartTime:        start,
		DistanceMeters:   10000,
		DurationSeconds:  3000,
		AvgPaceSecPerKm:  &avg,
		BestEfforts:      efforts,
	}
	if err := repo.Create(context.Background(), a, []byte("<x/>")); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return a
}

func TestRunningBestEfforts_HappyPath(t *testing.T) {
	h, _, repo := newTestHandler(t)

	at := func(s string) time.Time {
		tt, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}
	seedRunWithEfforts(t, repo, "r1", at("2026-04-18T06:45:00Z"), []ActivityBestEffort{
		{DistanceKey: "1mi", DurationSeconds: 332.4},
		{DistanceKey: "5k", DurationSeconds: 1184.7},
	})
	seedRunWithEfforts(t, repo, "r2", at("2026-05-22T07:12:11Z"), []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1300}, // slower 5K, must not win
	})

	req := httptest.NewRequest("GET", "/running/best-efforts", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningBestEfforts(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env bestEffortsEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.BestEfforts) != 2 {
		t.Fatalf("want 2 entries, got %d", len(env.Data.BestEfforts))
	}
	// Sorted shortest-first: 1mi then 5k.
	if env.Data.BestEfforts[0].DistanceKey != "1mi" || env.Data.BestEfforts[1].DistanceKey != "5k" {
		t.Errorf("order wrong: %+v", env.Data.BestEfforts)
	}

	mi := env.Data.BestEfforts[0]
	if mi.DistanceLabel != "1 Mile" || mi.DistanceMeters != 1609.344 {
		t.Errorf("1mi label/meters wrong: %+v", mi)
	}
	// pace_sec_per_km = duration / (meters/1000).
	wantPace := 332.4 / (1609.344 / 1000)
	if d := mi.PaceSecPerKm - wantPace; d > 0.001 || d < -0.001 {
		t.Errorf("pace_sec_per_km = %.4f, want %.4f", mi.PaceSecPerKm, wantPace)
	}

	fiveK := env.Data.BestEfforts[1]
	if fiveK.DurationSeconds != 1184.7 {
		t.Errorf("5k duration = %.2f, want 1184.7 (the faster run)", fiveK.DurationSeconds)
	}
}

func TestRunningBestEfforts_Empty(t *testing.T) {
	h, _, _ := newTestHandler(t)

	req := httptest.NewRequest("GET", "/running/best-efforts", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningBestEfforts(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// best_efforts must serialize as [] (not null).
	if !strings.Contains(w.Body.String(), `"best_efforts":[]`) {
		t.Errorf("empty body should contain \"best_efforts\":[], got %s", w.Body.String())
	}
}

func TestRunningBestEffortHistory_HappyPath(t *testing.T) {
	h, _, repo := newTestHandler(t)

	at := func(s string) time.Time {
		tt, _ := time.Parse(time.RFC3339, s)
		return tt
	}
	seedRunWithEfforts(t, repo, "r1", at("2026-02-18T07:08:00Z"), []ActivityBestEffort{{DistanceKey: "5k", DurationSeconds: 1312.7}})
	seedRunWithEfforts(t, repo, "r2", at("2026-01-12T07:02:00Z"), []ActivityBestEffort{{DistanceKey: "5k", DurationSeconds: 1340.2}})

	req := httptest.NewRequest("GET", "/running/best-efforts/5k/history", nil)
	req = withParam(req, "distance_key", "5k")
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningBestEffortHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env bestEffortHistoryEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.DistanceKey != "5k" || env.Data.DistanceLabel != "5K" || env.Data.DistanceMeters != 5000 {
		t.Errorf("history meta wrong: %+v", env.Data)
	}
	if len(env.Data.Points) != 2 {
		t.Fatalf("want 2 points, got %d", len(env.Data.Points))
	}
	if env.Data.Points[0].ActivityStartTime.After(env.Data.Points[1].ActivityStartTime) {
		t.Errorf("points not ascending: %+v", env.Data.Points)
	}
}

func TestRunningBestEffortHistory_UnknownDistanceKey(t *testing.T) {
	h, _, _ := newTestHandler(t)

	req := httptest.NewRequest("GET", "/running/best-efforts/15k/history", nil)
	req = withParam(req, "distance_key", "15k")
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningBestEffortHistory(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "unknown_distance_key" {
		t.Errorf("code = %q, want unknown_distance_key", env.Code)
	}
}

// --- running max-effort estimates ---------------------------------------

type maxEffortSummaryEnvelope struct {
	Message string                   `json:"message"`
	Data    maxEffortSummaryResponse `json:"data"`
}

type maxEffortDetailEnvelope struct {
	Message string                  `json:"message"`
	Data    maxEffortDetailResponse `json:"data"`
}

// fixedMaxEffortNow is the injected clock for the max-effort tests: well
// after the seeded efforts so recency weighting is deterministic.
var fixedMaxEffortNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func atRFC(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

// seedRunFull inserts a running activity with an explicit total distance so
// source classification (effort vs. activity distance) can be exercised.
func seedRunFull(t *testing.T, repo *SQLiteRepository, source string, start time.Time, distanceMeters float64, efforts []ActivityBestEffort) *Activity {
	t.Helper()
	avg := 300.0
	a := &Activity{
		UserID:           testUserID,
		ActivityType:     ActivityRunning,
		IngestSource:     IngestManualTCX,
		SourceActivityID: source,
		StartTime:        start,
		DistanceMeters:   distanceMeters,
		DurationSeconds:  3000,
		AvgPaceSecPerKm:  &avg,
		BestEfforts:      efforts,
	}
	if err := repo.Create(context.Background(), a, []byte("<x/>")); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return a
}

// seedMultiDistance seeds a user with a spread of efforts across several
// distances and dates, enough for the engine to fit a curve.
func seedMultiDistance(t *testing.T, repo *SQLiteRepository) {
	t.Helper()
	// A 1mi window inside a 5k run, a 5k race, and a 10k race — multi-distance
	// evidence on distinct dates.
	seedRunFull(t, repo, "me1", atRFC(t, "2026-03-10T07:00:00Z"), 5000, []ActivityBestEffort{
		{DistanceKey: "1mi", DurationSeconds: 330},
		{DistanceKey: "5k", DurationSeconds: 1180},
	})
	seedRunFull(t, repo, "me2", atRFC(t, "2026-04-15T07:00:00Z"), 10000, []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1170},
		{DistanceKey: "10k", DurationSeconds: 2500},
	})
	seedRunFull(t, repo, "me3", atRFC(t, "2026-05-20T07:00:00Z"), 10000, []ActivityBestEffort{
		{DistanceKey: "10k", DurationSeconds: 2460},
	})
}

func TestRunningMaxEffort_SummaryHappyPath(t *testing.T) {
	h, _, repo := newTestHandler(t)
	h.now = func() time.Time { return fixedMaxEffortNow }
	seedMultiDistance(t, repo)

	req := httptest.NewRequest("GET", "/running/max-effort", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMaxEffort(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env maxEffortSummaryEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.EstimatorVersion != "1.0.0" {
		t.Errorf("estimator_version = %q, want 1.0.0", env.Data.EstimatorVersion)
	}
	if len(env.Data.Distances) != 6 {
		t.Fatalf("distances len = %d, want 6", len(env.Data.Distances))
	}
	// At least one fitted_curve with a non-null estimate.
	sawFitted := false
	for _, d := range env.Data.Distances {
		if d.Basis != nil && *d.Basis == "fitted_curve" && d.EstimateSeconds != nil {
			sawFitted = true
		}
	}
	if !sawFitted {
		t.Errorf("expected at least one fitted_curve with a non-null estimate: %+v", env.Data.Distances)
	}
	// 5k was seeded → actual_best present.
	for _, d := range env.Data.Distances {
		if d.DistanceKey == "5k" {
			if d.ActualBestSeconds == nil || *d.ActualBestSeconds != 1170 {
				t.Errorf("5k actual_best_seconds = %v, want 1170", d.ActualBestSeconds)
			}
		}
	}
}

func TestRunningMaxEffort_SummaryNeverRanDistance(t *testing.T) {
	h, _, repo := newTestHandler(t)
	h.now = func() time.Time { return fixedMaxEffortNow }
	// Only a single 5k effort — marathon is never directly run, but the
	// engine can still extrapolate. To get a genuine insufficient/null case
	// we seed nothing and assert the all-null shape with no actual best.
	seedRunFull(t, repo, "only5k", atRFC(t, "2026-05-01T07:00:00Z"), 5000, []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1200},
	})

	req := httptest.NewRequest("GET", "/running/max-effort", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMaxEffort(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env maxEffortSummaryEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// marathon was never run → no actual best at that distance.
	for _, d := range env.Data.Distances {
		if d.DistanceKey == "marathon" {
			if d.ActualBestSeconds != nil {
				t.Errorf("marathon actual_best_seconds = %v, want nil", *d.ActualBestSeconds)
			}
		}
	}
}

func TestRunningMaxEffort_SummaryEmptyUserNullEstimates(t *testing.T) {
	h, _, _ := newTestHandler(t)
	h.now = func() time.Time { return fixedMaxEffortNow }

	req := httptest.NewRequest("GET", "/running/max-effort", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMaxEffort(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env maxEffortSummaryEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Distances) != 6 {
		t.Fatalf("distances len = %d, want 6", len(env.Data.Distances))
	}
	for _, d := range env.Data.Distances {
		if d.EstimateSeconds != nil || d.Basis != nil || d.ActualBestSeconds != nil {
			t.Errorf("empty user should have all-null fields, got %+v", d)
		}
	}
}

func TestRunningMaxEffortDetail_HappyPath(t *testing.T) {
	h, _, repo := newTestHandler(t)
	h.now = func() time.Time { return fixedMaxEffortNow }
	seedMultiDistance(t, repo)

	req := httptest.NewRequest("GET", "/running/max-effort/5k", nil)
	req = withParam(req, "distance_key", "5k")
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMaxEffortDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env maxEffortDetailEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Estimate == nil {
		t.Fatalf("expected non-null estimate block; body=%s", w.Body.String())
	}
	if env.Data.EstimatorVersion != "1.0.0" {
		t.Errorf("estimator_version = %q, want 1.0.0", env.Data.EstimatorVersion)
	}
	// estimate_history has >= 1 point and is ascending by as_of.
	if len(env.Data.EstimateHistory) < 1 {
		t.Fatalf("estimate_history empty")
	}
	for i := 1; i < len(env.Data.EstimateHistory); i++ {
		if env.Data.EstimateHistory[i-1].AsOf > env.Data.EstimateHistory[i].AsOf {
			t.Errorf("estimate_history not ascending: %+v", env.Data.EstimateHistory)
		}
	}
	// attempts: ascending, pace derived, source present.
	if len(env.Data.Attempts) != 2 {
		t.Fatalf("attempts len = %d, want 2", len(env.Data.Attempts))
	}
	for i := 1; i < len(env.Data.Attempts); i++ {
		if env.Data.Attempts[i-1].AchievedAt > env.Data.Attempts[i].AchievedAt {
			t.Errorf("attempts not ascending: %+v", env.Data.Attempts)
		}
	}
	first := env.Data.Attempts[0]
	wantPace := first.DurationSeconds / (5000.0 / 1000)
	if d := first.PaceSecPerKm - wantPace; d > 0.001 || d < -0.001 {
		t.Errorf("pace_sec_per_km = %v, want %v", first.PaceSecPerKm, wantPace)
	}
	if first.Source == "" {
		t.Errorf("source empty on attempt %+v", first)
	}
	if first.ActivityID == "" {
		t.Errorf("activity_id empty on attempt %+v", first)
	}
	// stats.gap_seconds = estimate - best.
	if env.Data.ActualBest == nil {
		t.Fatalf("expected actual_best for 5k")
	}
	if env.Data.Stats.GapSeconds == nil || env.Data.Stats.EstimatedMaxEffortSeconds == nil || env.Data.Stats.CurrentBestSeconds == nil {
		t.Fatalf("stats numeric fields should be present: %+v", env.Data.Stats)
	}
	wantGap := *env.Data.Stats.EstimatedMaxEffortSeconds - *env.Data.Stats.CurrentBestSeconds
	if d := *env.Data.Stats.GapSeconds - wantGap; d > 0.001 || d < -0.001 {
		t.Errorf("gap_seconds = %v, want %v", *env.Data.Stats.GapSeconds, wantGap)
	}
}

func TestRunningMaxEffortDetail_UnknownDistanceKey(t *testing.T) {
	h, _, _ := newTestHandler(t)
	h.now = func() time.Time { return fixedMaxEffortNow }

	req := httptest.NewRequest("GET", "/running/max-effort/15k", nil)
	req = withParam(req, "distance_key", "15k")
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMaxEffortDetail(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	var env codeEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != "unknown_distance_key" {
		t.Errorf("code = %q, want unknown_distance_key", env.Code)
	}
}

func TestRunningMaxEffortDetail_InsufficientData(t *testing.T) {
	h, _, _ := newTestHandler(t)
	h.now = func() time.Time { return fixedMaxEffortNow }
	// No efforts at all → marathon detail has insufficient data.

	req := httptest.NewRequest("GET", "/running/max-effort/marathon", nil)
	req = withParam(req, "distance_key", "marathon")
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMaxEffortDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env maxEffortDetailEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Estimate != nil {
		t.Errorf("expected null estimate for insufficient data, got %+v", env.Data.Estimate)
	}
	if env.Data.Stats.Confidence != "insufficient_data" {
		t.Errorf("stats.confidence = %q, want insufficient_data", env.Data.Stats.Confidence)
	}
}

// --- heart rate zones ----------------------------------------------------

func TestGetActivity_HeartRateZones(t *testing.T) {
	h, _, _ := newTestHandler(t)
	h.SetHRZonesEngine(testHRZonesEngine(), 90*24*time.Hour)

	imp := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if imp.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201; body=%s", imp.Code, imp.Body.String())
	}
	var impEnv activityEnvelope
	if err := json.Unmarshal(imp.Body.Bytes(), &impEnv); err != nil {
		t.Fatalf("decode import: %v", err)
	}
	id := impEnv.Data.ID

	get := httptest.NewRequest("GET", "/activities/"+id, nil)
	get = withParam(get.WithContext(authctx.WithUserID(get.Context(), testUserID)), "id", id)
	w := httptest.NewRecorder()
	h.get(w, get)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var env activityEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	hz := env.Data.HeartRateZones
	if hz == nil {
		t.Fatalf("expected heart_rate_zones block; body=%s", w.Body.String())
	}
	if hz.Model != "percent_max_hr" {
		t.Errorf("model = %q, want percent_max_hr", hz.Model)
	}
	if len(hz.Zones) != 5 {
		t.Fatalf("zones len = %d, want 5", len(hz.Zones))
	}
	var sum float64
	for _, z := range hz.Zones {
		sum += z.TimePct
	}
	if d := sum - 1.0; d > 1e-6 || d < -1e-6 {
		t.Errorf("sum(time_pct) = %v, want ~1.0", sum)
	}
	// Single freshly-imported run is a cold start: the reference is the
	// population default, so confidence is "estimated" and calibrating is true.
	if hz.ReferenceConfidence != "estimated" {
		t.Errorf("reference_confidence = %q, want estimated", hz.ReferenceConfidence)
	}
	if !hz.Calibrating {
		t.Errorf("calibrating = %v, want true for a single cold-start run", hz.Calibrating)
	}
}

func TestGetActivity_NoHR_OmitsBlock(t *testing.T) {
	h, _, repo := newTestHandler(t)
	h.SetHRZonesEngine(testHRZonesEngine(), 90*24*time.Hour)

	a := &Activity{
		UserID:           testUserID,
		ActivityType:     ActivityRunning,
		IngestSource:     IngestManualTCX,
		SourceActivityID: "no-hr",
		StartTime:        time.Now().UTC(),
		DistanceMeters:   5000,
		DurationSeconds:  1500,
		Trackpoints: []Trackpoint{
			{Sequence: 0, ElapsedSeconds: 0, DistanceMeters: 0},
			{Sequence: 1, ElapsedSeconds: 10, DistanceMeters: 30},
		},
	}
	if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	get := httptest.NewRequest("GET", "/activities/"+a.ID, nil)
	get = withParam(get.WithContext(authctx.WithUserID(get.Context(), testUserID)), "id", a.ID)
	w := httptest.NewRecorder()
	h.get(w, get)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "heart_rate_zones") {
		t.Errorf("expected heart_rate_zones key absent for a no-HR run; body=%s", w.Body.String())
	}
}
