package running

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

type sessionEnvelope struct {
	Message string     `json:"message"`
	Data    sessionDTO `json:"data"`
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
	Error             string `json:"error"`
	Code              string `json:"code"`
	ExistingSessionID string `json:"existing_session_id"`
}

// --- helpers -------------------------------------------------------

func newTestHandler() (*Handler, *MemoryArchiver, *MemoryRepository) {
	arch := NewMemoryArchiver()
	repo := NewMemoryRepository(arch)
	return NewHandler(repo), arch, repo
}

// multipartBody builds a multipart/form-data body with the TCX bytes under
// the "file" field and returns the body plus its Content-Type header.
func multipartBody(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "run.tcx")
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

// doImport drives importSession with a multipart upload of data.
func doImport(t *testing.T, h *Handler, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartBody(t, data)
	req := httptest.NewRequest("POST", "/running/sessions/imports", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.importSession(w, req)
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
	h, _, _ := newTestHandler()
	w := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var env sessionEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.ID == "" {
		t.Error("expected non-empty session id")
	}
	if env.Data.GarminActivityID != "2026-01-02T08:00:00Z" {
		t.Errorf("garmin_activity_id = %q", env.Data.GarminActivityID)
	}
	if env.Data.DistanceMeters != 5000 {
		t.Errorf("distance = %v, want 5000", env.Data.DistanceMeters)
	}
	if len(env.Data.Trackpoints) == 0 {
		t.Error("expected trackpoints on import response")
	}
}

func TestImportDuplicate(t *testing.T) {
	h, _, _ := newTestHandler()
	first := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first import status = %d", first.Code)
	}
	var firstEnv sessionEnvelope
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
	if dup.Code != "duplicate_run" {
		t.Errorf("code = %q, want duplicate_run", dup.Code)
	}
	if dup.ExistingSessionID != firstEnv.Data.ID {
		t.Errorf("existing_session_id = %q, want %q", dup.ExistingSessionID, firstEnv.Data.ID)
	}
}

// --- validation slugs ----------------------------------------------

func TestImportValidationSlugs(t *testing.T) {
	cases := []struct {
		fixture string
		code    string
	}{
		{"biking.tcx", SlugNotRunning},
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
	// Build a >10MB multipart body. The actual bytes need not be valid TCX:
	// MaxBytesReader trips during ParseMultipartForm before any parse.
	big := bytes.Repeat([]byte("a"), maxTCXBytes+1024)
	body, ct := multipartBody(t, big)
	req := httptest.NewRequest("POST", "/running/sessions/imports", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.importSession(w, req)

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
	req := httptest.NewRequest("POST", "/running/sessions/imports", strings.NewReader(`{"foo":"bar"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.importSession(w, req)

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
	arch.PutErr = context.DeadlineExceeded // any non-nil error forces a Put failure
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
	// Rollback: no session persisted.
	got, err := repo.List(context.Background(), testUserID, 10, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 sessions after storage failure, got %d", len(got))
	}
}

// --- list pagination via before= -----------------------------------

func TestListPagination(t *testing.T) {
	h, arch, repo := newTestHandler()
	_ = arch
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// Seed 3 sessions on distinct days (newest last). Direct Create is
	// simpler than minting 3 TCX files with distinct activity IDs.
	for i := 0; i < 3; i++ {
		s := &Session{
			UserID:           testUserID,
			GarminActivityID: "act-" + string(rune('a'+i)),
			StartTime:        base.AddDate(0, 0, i),
			DistanceMeters:   1000,
			DurationSeconds:  300,
			AvgPaceSecPerKm:  300,
		}
		if err := repo.Create(context.Background(), s, []byte("<tcx/>")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// Page 1: limit=2 → 2 newest + a next_before cursor.
	req := httptest.NewRequest("GET", "/running/sessions?limit=2", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.listSessions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("page1 status = %d; body=%s", w.Code, w.Body.String())
	}
	var p1 listEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &p1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(p1.Data.Sessions) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(p1.Data.Sessions))
	}
	if p1.Data.NextBefore == nil {
		t.Fatal("expected non-nil next_before on full page")
	}

	// Page 2: before=cursor → the remaining session, null cursor.
	req2 := httptest.NewRequest("GET", "/running/sessions?limit=2&before="+*p1.Data.NextBefore, nil)
	req2 = req2.WithContext(authctx.WithUserID(req2.Context(), testUserID))
	w2 := httptest.NewRecorder()
	h.listSessions(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d; body=%s", w2.Code, w2.Body.String())
	}
	var p2 listEnvelope
	if err := json.Unmarshal(w2.Body.Bytes(), &p2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(p2.Data.Sessions) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(p2.Data.Sessions))
	}
	if p2.Data.NextBefore != nil {
		t.Errorf("expected nil next_before on last page, got %q", *p2.Data.NextBefore)
	}
}

// --- list date-range (since/until) ---------------------------------

func TestListSinceUntil(t *testing.T) {
	h, _, repo := newTestHandler()
	// Seed five sessions across three calendar months. The calendar's
	// month-view query has the shape since=<monthStart>&until=<nextMonthStart>;
	// we assert that only the sessions whose start_time falls inside the
	// half-open interval [since, until) come back, regardless of position.
	starts := []time.Time{
		time.Date(2026, 2, 27, 7, 0, 0, 0, time.UTC), // before range
		time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC),  // inside (lower edge, inclusive)
		time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC), // inside
		time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC),// inside (top of month)
		time.Date(2026, 4, 1, 6, 0, 0, 0, time.UTC),  // after range (upper edge, exclusive)
	}
	for i, st := range starts {
		s := &Session{
			UserID:           testUserID,
			GarminActivityID: "range-" + string(rune('a'+i)),
			StartTime:        st,
			DistanceMeters:   1000,
			DurationSeconds:  300,
		}
		if err := repo.Create(context.Background(), s, []byte("<tcx/>")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	url := "/running/sessions?since=2026-03-01T00:00:00Z&until=2026-04-01T00:00:00Z"
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.listSessions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var env listEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := len(env.Data.Sessions), 3; got != want {
		t.Fatalf("range result count = %d, want %d (March-only)", got, want)
	}
	// Range queries return a complete result; no cursor is meaningful.
	if env.Data.NextBefore != nil {
		t.Errorf("range query returned a next_before cursor; range results are complete")
	}

	// Mixing range params with cursor params is a programmer error; reject.
	mix := httptest.NewRequest("GET", url+"&before=2026-03-15T00:00:00Z", nil)
	mix = mix.WithContext(authctx.WithUserID(mix.Context(), testUserID))
	mw := httptest.NewRecorder()
	h.listSessions(mw, mix)
	if mw.Code != http.StatusBadRequest {
		t.Errorf("mixed since+before status = %d, want 400", mw.Code)
	}
}

// --- rename --------------------------------------------------------

func TestRenameHandler(t *testing.T) {
	h, _, repo := newTestHandler()
	s := &Session{UserID: testUserID, GarminActivityID: "x", StartTime: time.Now().UTC()}
	if err := repo.Create(context.Background(), s, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Happy path.
	req := httptest.NewRequest("PATCH", "/running/sessions/"+s.ID, strings.NewReader(`{"name":"Tempo Run"}`))
	req = withParam(req.WithContext(authctx.WithUserID(req.Context(), testUserID)), "id", s.ID)
	w := httptest.NewRecorder()
	h.renameSession(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rename status = %d; body=%s", w.Code, w.Body.String())
	}
	var env sessionEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Name == nil || *env.Data.Name != "Tempo Run" {
		t.Errorf("name = %v, want Tempo Run", env.Data.Name)
	}

	// Blank and too-long → 400.
	for _, body := range []string{`{"name":""}`, `{"name":"` + strings.Repeat("a", 201) + `"}`} {
		req := httptest.NewRequest("PATCH", "/running/sessions/"+s.ID, strings.NewReader(body))
		req = withParam(req.WithContext(authctx.WithUserID(req.Context(), testUserID)), "id", s.ID)
		w := httptest.NewRecorder()
		h.renameSession(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("rename %q status = %d, want 400", body, w.Code)
		}
	}

	// Missing id → 404.
	req404 := httptest.NewRequest("PATCH", "/running/sessions/nope", strings.NewReader(`{"name":"x"}`))
	req404 = withParam(req404.WithContext(authctx.WithUserID(req404.Context(), testUserID)), "id", "nope")
	w404 := httptest.NewRecorder()
	h.renameSession(w404, req404)
	if w404.Code != http.StatusNotFound {
		t.Errorf("rename missing status = %d, want 404", w404.Code)
	}
}

// --- delete then get -----------------------------------------------

func TestDeleteThenGet(t *testing.T) {
	h, _, repo := newTestHandler()
	s := &Session{UserID: testUserID, GarminActivityID: "x", StartTime: time.Now().UTC()}
	if err := repo.Create(context.Background(), s, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	del := httptest.NewRequest("DELETE", "/running/sessions/"+s.ID, nil)
	del = withParam(del.WithContext(authctx.WithUserID(del.Context(), testUserID)), "id", s.ID)
	wd := httptest.NewRecorder()
	h.deleteSession(wd, del)
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", wd.Code)
	}

	get := httptest.NewRequest("GET", "/running/sessions/"+s.ID, nil)
	get = withParam(get.WithContext(authctx.WithUserID(get.Context(), testUserID)), "id", s.ID)
	wg := httptest.NewRecorder()
	h.getSession(wg, get)
	if wg.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", wg.Code)
	}
}

// --- metrics -------------------------------------------------------

func TestMetricsHandler(t *testing.T) {
	h, _, repo := newTestHandler()
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		s := &Session{
			UserID:           testUserID,
			GarminActivityID: "m-" + string(rune('a'+i)),
			StartTime:        now.AddDate(0, 0, -i),
			DistanceMeters:   3000,
			DurationSeconds:  900,
			AvgPaceSecPerKm:  300,
		}
		if err := repo.Create(context.Background(), s, []byte("<tcx/>")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	req := httptest.NewRequest("GET", "/running/metrics?timezone=America/Denver", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.metrics(w, req)
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

func TestMetricsHandlerMissingTimezone(t *testing.T) {
	h, _, _ := newTestHandler()
	req := httptest.NewRequest("GET", "/running/metrics", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), testUserID))
	w := httptest.NewRecorder()
	h.metrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
