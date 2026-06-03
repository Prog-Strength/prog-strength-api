package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// envelope mirrors the success shape from httpresp, with Data typed as a
// *User so we can assert on the returned account directly.
type envelope struct {
	Message string `json:"message"`
	Data    *User  `json:"data"`
}

func TestGetMe_ReturnsUser(t *testing.T) {
	repo := NewMemoryRepository()
	u := &User{Email: "lifter@example.com", DisplayName: "Lifter", WeightUnit: WeightUnitPounds}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), u.ID))
	w := httptest.NewRecorder()

	NewHandler(repo).getMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var got envelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data == nil {
		t.Fatalf("data is nil, body=%s", w.Body.String())
	}
	if got.Data.ID != u.ID || got.Data.Email != "lifter@example.com" || got.Data.WeightUnit != WeightUnitPounds {
		t.Fatalf("unexpected user: %+v", got.Data)
	}
}

func TestGetMe_NotFound(t *testing.T) {
	repo := NewMemoryRepository()

	req := httptest.NewRequest("GET", "/me", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "nonexistent"))
	w := httptest.NewRecorder()

	NewHandler(repo).getMe(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404, body=%s", w.Code, w.Body.String())
	}
}
