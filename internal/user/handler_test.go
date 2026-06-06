package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	u := &User{Email: "lifter@example.com", DisplayName: "Lifter", WeightUnit: WeightUnitPounds, DistanceUnit: DistanceUnitMiles}
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
	if got.Data.DistanceUnit != DistanceUnitMiles {
		t.Fatalf("distance_unit: got %q want %q", got.Data.DistanceUnit, DistanceUnitMiles)
	}
}

// seedUser is a small helper to insert a fully-valid user for handler tests.
func seedUser(t *testing.T, repo Repository) *User {
	t.Helper()
	u := &User{Email: "lifter@example.com", DisplayName: "Lifter", WeightUnit: WeightUnitPounds, DistanceUnit: DistanceUnitMiles}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// patchMe drives the updateMe handler with the given JSON body for the user.
func patchMe(repo Repository, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PATCH", "/me", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	NewHandler(repo).updateMe(w, req)
	return w
}

func TestUpdateMe_UpdatesDistanceUnit(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"distance_unit":"km"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var got envelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data == nil || got.Data.DistanceUnit != DistanceUnitKilometers {
		t.Fatalf("distance_unit not updated: %+v", got.Data)
	}
	// Other prefs untouched.
	if got.Data.WeightUnit != WeightUnitPounds || got.Data.DisplayName != "Lifter" {
		t.Fatalf("unexpected mutation: %+v", got.Data)
	}
}

func TestUpdateMe_InvalidDistanceUnit(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"distance_unit":"furlongs"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}

	// Persisted value should be unchanged.
	after, err := repo.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if after.DistanceUnit != DistanceUnitMiles {
		t.Fatalf("distance_unit mutated on invalid update: %q", after.DistanceUnit)
	}
}

func TestUpdateMe_DisplayNameOnlyLeavesUnitsUnchanged(t *testing.T) {
	repo := NewMemoryRepository()
	u := seedUser(t, repo)

	w := patchMe(repo, u.ID, `{"display_name":"New Name"}`)
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
	if got.Data.DisplayName != "New Name" {
		t.Fatalf("display_name not updated: %+v", got.Data)
	}
	if got.Data.WeightUnit != WeightUnitPounds || got.Data.DistanceUnit != DistanceUnitMiles {
		t.Fatalf("units changed unexpectedly: %+v", got.Data)
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
