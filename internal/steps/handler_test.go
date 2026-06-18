package steps

import (
	"context"
	"encoding/json"
	"fmt"
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

// listEnvelope mirrors the httpresp success shape with the list DTO typed.
type listEnvelope struct {
	Message string  `json:"message"`
	Data    listDTO `json:"data"`
}

type errEnvelope struct {
	Error string `json:"error"`
}

// do routes a request through a chi router with the handler mounted, so
// URL params populate exactly as in production. Runs as userID-in-context.
func do(t *testing.T, repo Repository, userID, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(repo)
	r := chi.NewRouter()
	h.Mount(r)

	var reqBody *strings.Reader
	if body == "" {
		reqBody = strings.NewReader("")
	} else {
		reqBody = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reqBody)
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
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

func assertBadRequest(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	var got errEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error == "" {
		t.Errorf("expected non-empty error message, body=%s", w.Body.String())
	}
}

// --- PUT /steps/{date} -------------------------------------------------

func TestUpsertHandler_HappyPath(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := do(t, repo, "u1", "PUT", "/steps/2026-06-14", `{"steps":8400}`)
	got := decodeEntry(t, w)
	if got.Date != "2026-06-14" {
		t.Errorf("date = %q, want 2026-06-14", got.Date)
	}
	if got.Steps != 8400 {
		t.Errorf("steps = %d, want 8400", got.Steps)
	}
	if got.ID == "" {
		t.Error("expected populated id")
	}
}

func TestUpsertHandler_ReLogOverwrites(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	_ = decodeEntry(t, do(t, repo, "u1", "PUT", "/steps/2026-06-14", `{"steps":8400}`))
	got := decodeEntry(t, do(t, repo, "u1", "PUT", "/steps/2026-06-14", `{"steps":12000}`))
	if got.Steps != 12000 {
		t.Errorf("re-log should overwrite: steps = %d, want 12000", got.Steps)
	}
}

func TestUpsertHandler_BadDate(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := do(t, repo, "u1", "PUT", "/steps/2026-13-99", `{"steps":8400}`)
	assertBadRequest(t, w)
}

func TestUpsertHandler_FutureDateRejected(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	// Three days ahead of today's UTC date — past the one-day slack.
	future := time.Now().UTC().AddDate(0, 0, 3).Format(dateLayout)
	w := do(t, repo, "u1", "PUT", "/steps/"+future, `{"steps":8400}`)
	assertBadRequest(t, w)
}

func TestUpsertHandler_TomorrowAllowed(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	// One day ahead is within the slack for tz midnight crossing.
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(dateLayout)
	w := do(t, repo, "u1", "PUT", "/steps/"+tomorrow, `{"steps":8400}`)
	if w.Code != http.StatusOK {
		t.Fatalf("tomorrow should be allowed: got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestUpsertHandler_NegativeSteps(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := do(t, repo, "u1", "PUT", "/steps/2026-06-14", `{"steps":-1}`)
	assertBadRequest(t, w)
}

func TestUpsertHandler_StepsOverMax(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := do(t, repo, "u1", "PUT", "/steps/2026-06-14", fmt.Sprintf(`{"steps":%d}`, MaxSteps+1))
	assertBadRequest(t, w)
}

// --- DELETE /steps/{date} ----------------------------------------------

func TestDeleteHandler_NoContentThenNotFound(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	if _, err := repo.UpsertEntry(context.Background(), &Entry{UserID: "u1", Date: "2026-06-14", Steps: 8400}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := do(t, repo, "u1", "DELETE", "/steps/2026-06-14", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d want 204, body=%s", w.Code, w.Body.String())
	}
	// Second delete → 404.
	w = do(t, repo, "u1", "DELETE", "/steps/2026-06-14", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete absent: got %d want 404, body=%s", w.Code, w.Body.String())
	}
}

// --- GET /steps keyset shape -------------------------------------------

func TestListHandler_KeysetShape(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()
	for _, d := range []string{"2026-06-10", "2026-06-11", "2026-06-12"} {
		if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "u1", Date: d, Steps: 1000}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	w := do(t, repo, "u1", "GET", "/steps?limit=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: got %d, body=%s", w.Code, w.Body.String())
	}
	var got listEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data.Steps) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got.Data.Steps))
	}
	if got.Data.Steps[0].Date != "2026-06-12" {
		t.Errorf("expected newest first, got %q", got.Data.Steps[0].Date)
	}
	if got.Data.NextBefore == nil || *got.Data.NextBefore != "2026-06-11" {
		t.Errorf("next_before = %v, want 2026-06-11", got.Data.NextBefore)
	}
}

func TestListHandler_RangeNextBeforeNull(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "u1", Date: "2026-06-12", Steps: 1000}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := do(t, repo, "u1", "GET", "/steps?since=2026-06-10&until=2026-06-14", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: got %d, body=%s", w.Code, w.Body.String())
	}
	// next_before must serialize as null in range mode.
	if !strings.Contains(w.Body.String(), `"next_before":null`) {
		t.Errorf("range mode should yield next_before:null, body=%s", w.Body.String())
	}
}

func TestListHandler_BadSince(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := do(t, repo, "u1", "GET", "/steps?since=nope", "")
	assertBadRequest(t, w)
}

// --- Authz: cross-user isolation ---------------------------------------

func TestAuthz_UserBCannotSeeOrDeleteUserAEntries(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "user-a", Date: "2026-06-14", Steps: 8400}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// user-b's list does not see user-a's entries.
	w := do(t, repo, "user-b", "GET", "/steps?limit=10", "")
	var got listEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data.Steps) != 0 {
		t.Errorf("user-b should not see user-a entries, got %+v", got.Data.Steps)
	}

	// user-b deleting user-a's day → 404.
	w = do(t, repo, "user-b", "DELETE", "/steps/2026-06-14", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete: got %d want 404, body=%s", w.Code, w.Body.String())
	}
}
