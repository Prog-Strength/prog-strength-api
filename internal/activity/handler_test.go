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

func newTestHandler() (*Handler, *MemoryArchiver, *MemoryRepository) {
	arch := NewMemoryArchiver()
	repo := NewMemoryRepository(arch)
	return NewHandler(repo), arch, repo
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
	h, arch, _ := newTestHandler()
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

// A biking TCX no longer rejects with SlugNotRunning — the validator
// accepts any sport; the ingest pipeline classifies it as cycling.
func TestImportBikingClassifiesAsCycling(t *testing.T) {
	h, _, _ := newTestHandler()
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
	h, _, _ := newTestHandler()
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
			h, _, _ := newTestHandler()
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
	h, _, _ := newTestHandler()
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
	h, _, _ := newTestHandler()
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
	h, arch, repo := newTestHandler()
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
	h, _, repo := newTestHandler()
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
	h, _, repo := newTestHandler()
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
	h, _, repo := newTestHandler()
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
	h, _, repo := newTestHandler()
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
	h, _, repo := newTestHandler()
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
	h, _, _ := newTestHandler()
	req := httptest.NewRequest("GET", "/activities/running-metrics", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.runningMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
