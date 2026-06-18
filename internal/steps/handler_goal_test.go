package steps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// goalEnvelope mirrors the httpresp success shape with the goal DTO typed.
type goalEnvelope struct {
	Message string  `json:"message"`
	Data    goalDTO `json:"data"`
}

func newGoalHandler(t *testing.T) *Handler {
	t.Helper()
	return NewHandler(NewSQLiteRepository(dbtest.New(t)))
}

func getGoal(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/me/steps-goal", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.getMyStepsGoal(w, req)
	return w
}

func putGoal(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/me/steps-goal", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	h.putMyStepsGoal(w, req)
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

func TestGetMyStepsGoal_EmptyState(t *testing.T) {
	h := newGoalHandler(t)
	w := getGoal(t, h)
	// Snapshot the raw body before decoding consumes the buffer.
	raw := w.Body.String()
	got := decodeGoal(t, w)
	if got.Goal != 0 {
		t.Errorf("never-set goal = %d, want 0", got.Goal)
	}
	if got.CreatedAt != nil || got.UpdatedAt != nil {
		t.Errorf("never-set should have nil timestamps, got %v / %v", got.CreatedAt, got.UpdatedAt)
	}
	// Body must carry null timestamps explicitly.
	if !strings.Contains(raw, `"created_at":null`) {
		t.Errorf("expected created_at:null, body=%s", raw)
	}
}

func TestPutMyStepsGoal_HappyPath(t *testing.T) {
	h := newGoalHandler(t)
	got := decodeGoal(t, putGoal(t, h, `{"goal":10000}`))
	if got.Goal != 10000 {
		t.Errorf("goal = %d, want 10000", got.Goal)
	}
	if got.CreatedAt == nil || got.UpdatedAt == nil {
		t.Errorf("expected non-nil timestamps, got %v / %v", got.CreatedAt, got.UpdatedAt)
	}
}

func TestPutMyStepsGoal_Validation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"goal zero", `{"goal":0}`},
		{"goal negative", `{"goal":-5}`},
		{"goal too large", `{"goal":200001}`},
		{"goal missing", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newGoalHandler(t)
			w := putGoal(t, h, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPutMyStepsGoal_SecondPutWins(t *testing.T) {
	h := newGoalHandler(t)
	first := decodeGoal(t, putGoal(t, h, `{"goal":10000}`))
	if first.Goal != 10000 {
		t.Fatalf("first goal = %d, want 10000", first.Goal)
	}
	second := decodeGoal(t, putGoal(t, h, `{"goal":12500}`))
	if second.Goal != 12500 {
		t.Fatalf("second put did not replace: %d", second.Goal)
	}
	got := decodeGoal(t, getGoal(t, h))
	if got.Goal != 12500 {
		t.Errorf("read-back mismatch: %d, want 12500", got.Goal)
	}
}
