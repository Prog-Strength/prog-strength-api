package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// putLayout drives PUT /dashboard/layout for userID with the given raw JSON
// body and an auth context. When userID is empty the request is sent WITHOUT an
// auth context, exercising the missing-user path.
func putLayout(t *testing.T, r *chi.Mux, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/dashboard/layout", strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPutLayout_Valid(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"tile_ids":["running","steps"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	want := []TileID{TileRunning, TileSteps}
	if len(got.TileIDs) != len(want) {
		t.Fatalf("stored tile ids = %v, want %v", got.TileIDs, want)
	}
	for i := range want {
		if got.TileIDs[i] != want[i] {
			t.Errorf("stored tile ids = %v, want %v", got.TileIDs, want)
			break
		}
	}
}

func TestPutLayout_UnknownID(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"tile_ids":["running","bogus"]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The error must name the offending id and enumerate the valid ids so the
	// client can self-correct.
	if !strings.Contains(body, "bogus") {
		t.Errorf("body %q does not mention the unknown id", body)
	}
	if !strings.Contains(body, string(TileRunning)) || !strings.Contains(body, string(TileStreak)) {
		t.Errorf("body %q does not list the valid ids", body)
	}
}

func TestPutLayout_DuplicateID(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"tile_ids":["running","running"]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "duplicate") {
		t.Errorf("body %q does not mention the duplicate", rec.Body.String())
	}
}

func TestPutLayout_Empty(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"tile_ids":[]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// An empty dashboard is a real, persisted preference: Get must resolve the
	// stored row (not ErrLayoutNotFound) and return a non-nil, len-0 slice.
	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	if got.TileIDs == nil {
		t.Fatal("stored tile ids nil, want non-nil empty slice")
	}
	if len(got.TileIDs) != 0 {
		t.Errorf("stored tile ids = %v, want empty", got.TileIDs)
	}
}

func TestPutLayout_MalformedJSON(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"tile_ids":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutLayout_NoAuth(t *testing.T) {
	r, _, _ := newTestEnv(t)

	// No auth context → the handler treats a missing user as a server fault
	// (auth middleware not applied), mirroring the summary handler.
	rec := putLayout(t, r, "", `{"tile_ids":["running"]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}
