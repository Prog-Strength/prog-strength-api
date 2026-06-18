package bodyweight

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
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// entryEnvelope mirrors the httpresp success shape with the entry DTO typed.
type entryEnvelope struct {
	Message string   `json:"message"`
	Data    entryDTO `json:"data"`
}

// updateEntry routes PUT /bodyweight/{id} through a chi router that has the
// handler mounted, so the {id} URL param is populated exactly as it is in
// production. The request runs as userID-in-context.
func updateEntry(t *testing.T, repo Repository, userID, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(repo, nil)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest("PUT", "/bodyweight/"+id, strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func seedEntry(t *testing.T, repo Repository, userID string, weight float64, unit user.WeightUnit) *Entry {
	t.Helper()
	e := &Entry{
		UserID:     userID,
		Weight:     weight,
		Unit:       unit,
		MeasuredAt: time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC),
	}
	if err := repo.Create(context.Background(), e); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return e
}

func decodeEntry(t *testing.T, w *httptest.ResponseRecorder) entryDTO {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	var got entryEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Data
}

func TestUpdateHandler_HappyPath(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	e := seedEntry(t, repo, "u1", 185, user.WeightUnitPounds)

	w := updateEntry(t, repo, "u1", e.ID, `{"weight":183.5}`)
	got := decodeEntry(t, w)
	if got.Weight != 183.5 {
		t.Errorf("weight = %v, want 183.5", got.Weight)
	}
	if got.Unit != "lb" {
		t.Errorf("unit = %q, want lb (preserved)", got.Unit)
	}
}

func TestUpdateHandler_SoftDeletedReturns404(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	e := seedEntry(t, repo, "u1", 180, user.WeightUnitPounds)
	if err := repo.Delete(context.Background(), "u1", e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	w := updateEntry(t, repo, "u1", e.ID, `{"weight":181}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateHandler_CrossUserReturns404(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	e := seedEntry(t, repo, "user-a", 180, user.WeightUnitPounds)
	// user-b must not be able to update user-a's entry.
	w := updateEntry(t, repo, "user-b", e.ID, `{"weight":999}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", w.Code, w.Body.String())
	}
	got, err := repo.Get(context.Background(), "user-a", e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Weight != 180 {
		t.Errorf("cross-user update leaked: weight = %v, want 180", got.Weight)
	}
}

func TestUpdateHandler_WeightZeroReturns400(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	e := seedEntry(t, repo, "u1", 180, user.WeightUnitPounds)
	w := updateEntry(t, repo, "u1", e.ID, `{"weight":0}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	var got goalErrEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error != "weight must be positive" {
		t.Errorf("error = %q, want %q", got.Error, "weight must be positive")
	}
}

func TestUpdateHandler_PartialPreservesWeightAndUnit(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	e := seedEntry(t, repo, "u1", 185, user.WeightUnitKilograms)

	newMeasured := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	w := updateEntry(t, repo, "u1", e.ID, `{"measured_at":"2026-06-01T08:00:00Z"}`)
	got := decodeEntry(t, w)
	if got.Weight != 185 || got.Unit != "kg" {
		t.Errorf("partial update should preserve weight/unit, got %+v", got)
	}
	if !got.MeasuredAt.Equal(newMeasured) {
		t.Errorf("measured_at = %v, want %v", got.MeasuredAt, newMeasured)
	}
}
