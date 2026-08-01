package bloodpressure

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// entryEnvelope mirrors the httpresp success shape with the entry DTO typed.
type entryEnvelope struct {
	Message string   `json:"message"`
	Data    entryDTO `json:"data"`
}

// listEnvelope mirrors the httpresp success shape with a list of DTOs typed.
type listEnvelope struct {
	Message string     `json:"message"`
	Data    []entryDTO `json:"data"`
}

type errEnvelope struct {
	Error string `json:"error"`
}

// doRequest routes a request through a chi router that has the handler
// mounted, so {id} URL params are populated exactly as in production. The
// request runs as userID-in-context.
func doRequest(t *testing.T, repo Repository, userID, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(repo)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func seedEntry(t *testing.T, repo Repository, userID string, systolic, diastolic int, pulse *int) *Entry {
	t.Helper()
	e := &Entry{
		UserID:     userID,
		Systolic:   systolic,
		Diastolic:  diastolic,
		Pulse:      pulse,
		MeasuredAt: time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC),
	}
	if err := repo.Create(context.Background(), e); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return e
}

func decodeEntry(t *testing.T, w *httptest.ResponseRecorder, wantStatus int) entryDTO {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status: got %d want %d, body=%s", w.Code, wantStatus, w.Body.String())
	}
	var got entryEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Data
}

func TestCreateHandler_MinimalDefaultsMeasuredAtAndSetsCategory(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))

	w := doRequest(t, repo, "u1", "POST", "/blood-pressure/", `{"systolic":118,"diastolic":76}`)
	got := decodeEntry(t, w, http.StatusCreated)

	if got.Category == "" {
		t.Errorf("category should be set, got empty")
	}
	if got.Category != CategoryNormal {
		t.Errorf("category = %q, want %q", got.Category, CategoryNormal)
	}
	if got.MeasuredAt.IsZero() {
		t.Errorf("measured_at should default to non-zero, got zero")
	}
	if got.Pulse != nil {
		t.Errorf("pulse should be nil, got %v", *got.Pulse)
	}
}

func TestCreateHandler_PulseAndMeasuredAtRoundTrip(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))

	w := doRequest(t, repo, "u1", "POST", "/blood-pressure/",
		`{"systolic":135,"diastolic":75,"pulse":62,"measured_at":"2026-06-01T08:00:00Z"}`)
	got := decodeEntry(t, w, http.StatusCreated)

	if got.Pulse == nil || *got.Pulse != 62 {
		t.Errorf("pulse = %v, want 62", got.Pulse)
	}
	want := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	if !got.MeasuredAt.Equal(want) {
		t.Errorf("measured_at = %v, want %v", got.MeasuredAt, want)
	}
	// 135 systolic lands in stage_1 by systolic even though diastolic reads normal.
	if got.Category != CategoryStage1 {
		t.Errorf("category = %q, want %q", got.Category, CategoryStage1)
	}
}

func TestCreateHandler_OutOfRangeReturns400(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))

	w := doRequest(t, repo, "u1", "POST", "/blood-pressure/", `{"systolic":400,"diastolic":80}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateHandler_InvertedPairReturns400(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))

	w := doRequest(t, repo, "u1", "POST", "/blood-pressure/", `{"systolic":120,"diastolic":130}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestListHandler_NewestFirstAndOmitsDeleted(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))

	older := &Entry{UserID: "u1", Systolic: 118, Diastolic: 76,
		MeasuredAt: time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)}
	if err := repo.Create(context.Background(), older); err != nil {
		t.Fatalf("seed older: %v", err)
	}
	newer := &Entry{UserID: "u1", Systolic: 122, Diastolic: 79,
		MeasuredAt: time.Date(2026, 6, 1, 7, 0, 0, 0, time.UTC)}
	if err := repo.Create(context.Background(), newer); err != nil {
		t.Fatalf("seed newer: %v", err)
	}

	w := doRequest(t, repo, "u1", "GET", "/blood-pressure/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	var got listEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 2 {
		t.Fatalf("len = %d, want 2", len(got.Data))
	}
	if got.Data[0].ID != newer.ID {
		t.Errorf("first entry = %q, want newest %q", got.Data[0].ID, newer.ID)
	}
	for _, e := range got.Data {
		if e.Category == "" {
			t.Errorf("entry %q has empty category", e.ID)
		}
	}

	// Soft-delete the newer entry; it must drop out of the list.
	del := doRequest(t, repo, "u1", "DELETE", "/blood-pressure/"+newer.ID, "")
	if del.Code != http.StatusOK {
		t.Fatalf("delete status: got %d want 200, body=%s", del.Code, del.Body.String())
	}

	w2 := doRequest(t, repo, "u1", "GET", "/blood-pressure/", "")
	var got2 listEnvelope
	if err := json.NewDecoder(w2.Body).Decode(&got2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got2.Data) != 1 {
		t.Fatalf("after delete len = %d, want 1", len(got2.Data))
	}
	if got2.Data[0].ID != older.ID {
		t.Errorf("remaining entry = %q, want %q", got2.Data[0].ID, older.ID)
	}
}

func TestUpdateHandler_OverlaysOnlySuppliedFields(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	pulse := 60
	e := seedEntry(t, repo, "u1", 120, 78, &pulse)

	w := doRequest(t, repo, "u1", "PUT", "/blood-pressure/"+e.ID, `{"systolic":128}`)
	got := decodeEntry(t, w, http.StatusOK)

	if got.Systolic != 128 {
		t.Errorf("systolic = %d, want 128", got.Systolic)
	}
	if got.Diastolic != 78 {
		t.Errorf("diastolic = %d, want 78 (unchanged)", got.Diastolic)
	}
	if got.Pulse == nil || *got.Pulse != 60 {
		t.Errorf("pulse = %v, want 60 (unchanged)", got.Pulse)
	}
	if got.Category == "" {
		t.Errorf("category should be set after update")
	}
}

func TestUpdateHandler_CrossUserReturns404(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	e := seedEntry(t, repo, "user-a", 120, 78, nil)

	w := doRequest(t, repo, "user-b", "PUT", "/blood-pressure/"+e.ID, `{"systolic":128}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", w.Code, w.Body.String())
	}
	got, err := repo.Get(context.Background(), "user-a", e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Systolic != 120 {
		t.Errorf("cross-user update leaked: systolic = %d, want 120", got.Systolic)
	}
}

func TestUpdateHandler_InvertingPairReturns400(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	e := seedEntry(t, repo, "u1", 120, 78, nil)

	// Raising diastolic above systolic must be caught by the combined
	// invariant even though only one of the pair changed.
	w := doRequest(t, repo, "u1", "PUT", "/blood-pressure/"+e.ID, `{"diastolic":130}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	var got errEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error == "" {
		t.Errorf("expected an error message, got empty")
	}
}
