package bodyweight

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// goalEnvelope mirrors the httpresp success shape with the goal DTO typed
// so handler tests can assert on the returned goal.
type goalEnvelope struct {
	Message string  `json:"message"`
	Data    goalDTO `json:"data"`
}

type goalErrEnvelope struct {
	Error string `json:"error"`
}

func newGoalHandler(t *testing.T) *Handler {
	t.Helper()
	// userRepo is unused by the goal handlers; nil is fine here.
	return NewHandler(NewSQLiteRepository(dbtest.New(t)), nil)
}

// getGoal drives getMyBodyweightGoal with userID-in-context.
func getGoal(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/me/bodyweight-goal", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.getMyBodyweightGoal(w, req)
	return w
}

// putGoal drives putMyBodyweightGoal with the given JSON body.
func putGoal(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/me/bodyweight-goal", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.putMyBodyweightGoal(w, req)
	return w
}

func decodeGoal(t *testing.T, w *httptest.ResponseRecorder) goalDTO {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	var got goalEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Data
}

func assertGoalBadRequest(t *testing.T, w *httptest.ResponseRecorder, wantMsg string) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	var got goalErrEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error != wantMsg {
		t.Fatalf("error = %q, want %q", got.Error, wantMsg)
	}
}

func TestGetMyBodyweightGoal_EmptyState(t *testing.T) {
	h := newGoalHandler(t)
	w := getGoal(t, h)
	got := decodeGoal(t, w)
	if got.Weight != 0 {
		t.Errorf("never-set weight = %v, want 0", got.Weight)
	}
	if got.Unit != "lb" {
		t.Errorf("never-set unit = %q, want lb", got.Unit)
	}
	if got.CreatedAt != nil || got.UpdatedAt != nil {
		t.Errorf("never-set should have nil timestamps, got %v / %v", got.CreatedAt, got.UpdatedAt)
	}
}

func TestPutMyBodyweightGoal_HappyPath(t *testing.T) {
	h := newGoalHandler(t)
	w := putGoal(t, h, `{"weight":175,"unit":"lb"}`)
	got := decodeGoal(t, w)
	if got.Weight != 175 {
		t.Errorf("weight = %v, want 175", got.Weight)
	}
	if got.Unit != "lb" {
		t.Errorf("unit = %q, want lb", got.Unit)
	}
	if got.CreatedAt == nil || got.UpdatedAt == nil {
		t.Errorf("expected non-nil timestamps, got %v / %v", got.CreatedAt, got.UpdatedAt)
	}
}

func TestPutMyBodyweightGoal_Validation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"weight zero", `{"weight":0,"unit":"lb"}`, "weight must be positive"},
		{"weight negative", `{"weight":-5,"unit":"lb"}`, "weight must be positive"},
		{"weight too large", `{"weight":2001,"unit":"lb"}`, "weight must be <= 2000"},
		{"unit unknown", `{"weight":175,"unit":"stone"}`, "unit must be 'lb' or 'kg'"},
		{"weight missing", `{"unit":"lb"}`, "weight must be positive"},
		{"unit missing", `{"weight":175}`, "unit must be 'lb' or 'kg'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newGoalHandler(t)
			w := putGoal(t, h, tt.body)
			assertGoalBadRequest(t, w, tt.wantMsg)
		})
	}
}

func TestPutMyBodyweightGoal_SecondPutWins(t *testing.T) {
	h := newGoalHandler(t)

	first := decodeGoal(t, putGoal(t, h, `{"weight":175,"unit":"lb"}`))
	if first.Weight != 175 {
		t.Fatalf("first weight = %v, want 175", first.Weight)
	}

	second := decodeGoal(t, putGoal(t, h, `{"weight":80,"unit":"kg"}`))
	if second.Weight != 80 || second.Unit != "kg" {
		t.Fatalf("second put did not replace: %+v", second)
	}

	// Read-back reflects the second write — set-replacement, not append.
	got := decodeGoal(t, getGoal(t, h))
	if got.Weight != 80 || got.Unit != "kg" {
		t.Errorf("read-back mismatch: %+v", got)
	}
}
