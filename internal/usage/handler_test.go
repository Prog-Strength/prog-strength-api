package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// usageEnvelope mirrors the httpresp success shape with Data typed as the
// usage payload so we can assert on it.
type usageEnvelope struct {
	Message string        `json:"message"`
	Data    usageResponse `json:"data"`
}

// newTestHandler builds a usage handler backed by a fresh telemetry.db
// and the SOW price table, with the clock pinned to a fixed UTC instant.
func newTestHandler(t *testing.T, capUSD float64, now time.Time) (*Handler, func(*testing.T, string, string, int64, int64, time.Time)) {
	t.Helper()
	l, conn := newTestLedger(t)
	h := NewHandler(l, capUSD)
	h.now = func() time.Time { return now }
	seedTurn := func(t *testing.T, id, userID string, in, out int64, at time.Time) {
		insertTurn(t, conn, id, userID, "claude-sonnet-4-6", in, out, 0, 0, at)
	}
	return h, seedTurn
}

func TestGetMyUsage_HappyPath(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	// cap $6.00; seed $3.00 (1M sonnet input) => 50%.
	h, seedTurn := newTestHandler(t, 6.00, now)
	seedTurn(t, "t1", "u-1", 1_000_000, 0, now)

	req := httptest.NewRequest("GET", "/me/usage?tz=UTC", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u-1"))
	w := httptest.NewRecorder()
	h.getMyUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	// Hard contract: the raw body must not contain a usd key.
	if strings.Contains(w.Body.String(), "usd") {
		t.Fatalf("response leaked a usd field: %s", w.Body.String())
	}

	var got usageEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Data.PercentUsed != 50 {
		t.Fatalf("percent_used: got %d want 50", got.Data.PercentUsed)
	}
	if got.Data.Capped {
		t.Fatalf("capped: got true want false")
	}
	if got.Data.ResetsAt != "2026-06-10T00:00:00Z" {
		t.Fatalf("resets_at: got %q want 2026-06-10T00:00:00Z", got.Data.ResetsAt)
	}
}

func TestGetMyUsage_MissingTzFallsBackToUTC(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	h, seedTurn := newTestHandler(t, 6.00, now)
	seedTurn(t, "t1", "u-1", 1_000_000, 0, now)

	req := httptest.NewRequest("GET", "/me/usage", nil) // no tz
	req = req.WithContext(authctx.WithUserID(req.Context(), "u-1"))
	w := httptest.NewRecorder()
	h.getMyUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	var got usageEnvelope
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got.Data.ResetsAt != "2026-06-10T00:00:00Z" {
		t.Fatalf("resets_at (UTC fallback): got %q", got.Data.ResetsAt)
	}
}

func TestGetMyUsage_BoundarySpendEqualsCap(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	// cap exactly $3.00; seed $3.00 => 100% and capped.
	h, seedTurn := newTestHandler(t, 3.00, now)
	seedTurn(t, "t1", "u-1", 1_000_000, 0, now)

	req := httptest.NewRequest("GET", "/me/usage?tz=UTC", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u-1"))
	w := httptest.NewRecorder()
	h.getMyUsage(w, req)

	var got usageEnvelope
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got.Data.PercentUsed != 100 || !got.Data.Capped {
		t.Fatalf("boundary: got percent=%d capped=%v want 100/true", got.Data.PercentUsed, got.Data.Capped)
	}
}

func TestGetMyUsage_OverCapClampsAt100(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	// cap $1.00; seed $3.00 => clamps to 100, capped.
	h, seedTurn := newTestHandler(t, 1.00, now)
	seedTurn(t, "t1", "u-1", 1_000_000, 0, now)

	req := httptest.NewRequest("GET", "/me/usage?tz=UTC", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u-1"))
	w := httptest.NewRecorder()
	h.getMyUsage(w, req)

	var got usageEnvelope
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got.Data.PercentUsed != 100 || !got.Data.Capped {
		t.Fatalf("over cap: got percent=%d capped=%v want 100/true", got.Data.PercentUsed, got.Data.Capped)
	}
}

func TestGetMyUsage_CapDisabledReportsUncapped(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	// cap 0 (disabled); even with spend, report 0% and not capped.
	h, seedTurn := newTestHandler(t, 0, now)
	seedTurn(t, "t1", "u-1", 1_000_000, 0, now)

	req := httptest.NewRequest("GET", "/me/usage?tz=UTC", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u-1"))
	w := httptest.NewRecorder()
	h.getMyUsage(w, req)

	var got usageEnvelope
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got.Data.PercentUsed != 0 || got.Data.Capped {
		t.Fatalf("disabled cap: got percent=%d capped=%v want 0/false", got.Data.PercentUsed, got.Data.Capped)
	}
}

// TestGetMyUsage_NoUserInContext asserts the handler treats a missing
// user (auth middleware not applied) as a server error, mirroring /me.
func TestGetMyUsage_NoUserInContext(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	h, _ := newTestHandler(t, 6.00, now)

	req := httptest.NewRequest("GET", "/me/usage", nil) // no user in ctx
	w := httptest.NewRecorder()
	h.getMyUsage(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500", w.Code)
	}
}

// TestGetMyUsage_MountedRoute exercises the chi-mounted path to confirm
// the route pattern is wired (the JWT middleware that enforces 401 lives
// in server wiring; here we provide the user via context as the
// middleware would).
func TestGetMyUsage_MountedRoute(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	h, seedTurn := newTestHandler(t, 6.00, now)
	seedTurn(t, "t1", "u-1", 1_000_000, 0, now)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), "u-1")))
			})
		})
		h.Mount(r)
	})

	req := httptest.NewRequest("GET", "/me/usage?tz=UTC", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("mounted route: got %d want 200, body=%s", w.Code, w.Body.String())
	}
}
