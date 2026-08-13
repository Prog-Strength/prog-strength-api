package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
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

	rec := putLayout(t, r, userID, `{"sections":[
		{"id":"a","title":"Endurance","collapsed":false,"tile_ids":["running","steps"]},
		{"id":"b","title":"Strength","collapsed":true,"tile_ids":["lifting"]}
	]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	assertSections(t, got.Sections, []Section{
		{ID: "a", Title: "Endurance", TileIDs: []TileID{TileRunning, TileSteps}},
		{ID: "b", Title: "Strength", Collapsed: true, TileIDs: []TileID{TileLifting}},
	})
}

// TestPutLayout_TileIDsBodyWrapped pins the compatibility path: a pre-055
// client (a browser tab left open across the deploy) sends a bare tile_ids
// body, and it is accepted and wrapped into one untitled section rather than
// 422-ing.
func TestPutLayout_TileIDsBodyWrapped(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"tile_ids":["running","steps"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	assertSections(t, got.Sections, []Section{
		{ID: "s1", Title: "", TileIDs: []TileID{TileRunning, TileSteps}},
	})
}

// TestPutLayout_RetiredTileAccepted is the other compatibility path: a browser
// tab served the catalog BEFORE recovery_trend was folded into hrv_balance
// keeps sending the retired id. Rejecting it would lose the user's whole edit
// over a rename they did not make, so the write path resolves it and stores the
// replacement.
func TestPutLayout_RetiredTileAccepted(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[{"id":"a","tile_ids":["steps","recovery_trend"]}]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	assertSections(t, got.Sections, []Section{
		{ID: "a", TileIDs: []TileID{TileSteps, TileHRVBalance}},
	})
}

// The same body from a client that ALSO had the replacement tile placed: the
// resolution collides, and one tile is the right answer — not a 422 for a
// duplicate the client never sent.
func TestPutLayout_RetiredTileCollidingWithItsReplacement(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[
		{"id":"a","tile_ids":["hrv_balance"]},
		{"id":"b","tile_ids":["recovery_trend","steps"]}
	]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	assertSections(t, got.Sections, []Section{
		{ID: "a", TileIDs: []TileID{TileHRVBalance}},
		{ID: "b", TileIDs: []TileID{TileSteps}},
	})
}

// TestPutLayout_RestingHRAccepted is the test that makes the catalog change
// load-bearing rather than cosmetic. Had resting_hr stayed out of Catalog,
// ValidTileID would have rejected it and this write would have 422'd — a user
// adding the tile from the tray would get an error toast and the tile would
// never persist. A web-only catalog entry cannot fix that, which is why this
// SOW touches the API.
func TestPutLayout_RestingHRAccepted(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[{"id":"a","tile_ids":["resting_hr"]}]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	assertSections(t, got.Sections, []Section{
		{ID: "a", TileIDs: []TileID{TileRestingHR}},
	})
}

// TestPutLayout_CalendarAccepted proves a client may place the calendar tile.
// It is tray-only — TestSummary_DefaultLayoutHasNoCalendarTile keeps it out of
// the default layout — so this write is the ONLY way it ever reaches a user's
// dashboard. Without the catalog entry ValidTileID would reject it and the
// tray click would 422.
func TestPutLayout_CalendarAccepted(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[{"id":"a","tile_ids":["calendar"]}]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	assertSections(t, got.Sections, []Section{
		{ID: "a", TileIDs: []TileID{TileCalendar}},
	})
}

func TestPutLayout_UnknownID(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[{"id":"a","tile_ids":["running","bogus"]}]}`)
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

func TestPutLayout_DuplicateTileWithinSection(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[{"id":"a","tile_ids":["running","running"]}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "duplicate") {
		t.Errorf("body %q does not mention the duplicate", rec.Body.String())
	}
}

// TestPutLayout_DuplicateTileAcrossSections is the invariant sections
// introduced: uniqueness is global, not per section. The same tile in two
// sections would render twice and desync the moment one copy is removed.
func TestPutLayout_DuplicateTileAcrossSections(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[
		{"id":"a","tile_ids":["running"]},
		{"id":"b","tile_ids":["steps","running"]}
	]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "running") {
		t.Errorf("body %q does not name the duplicated tile", rec.Body.String())
	}
}

func TestPutLayout_DuplicateSectionID(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[
		{"id":"a","tile_ids":["running"]},
		{"id":"a","tile_ids":["steps"]}
	]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "section id") {
		t.Errorf("body %q does not mention the section id", rec.Body.String())
	}
}

func TestPutLayout_EmptySectionID(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[{"id":"  ","tile_ids":["running"]}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutLayout_TooManySections(t *testing.T) {
	r, _, userID := newTestEnv(t)

	var b strings.Builder
	b.WriteString(`{"sections":[`)
	for i := 0; i <= MaxSections; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"s`)
		b.WriteString(string(rune('a' + i)))
		b.WriteString(`","tile_ids":[]}`)
	}
	b.WriteString(`]}`)

	rec := putLayout(t, r, userID, b.String())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "too many sections") {
		t.Errorf("body %q does not explain the cap", rec.Body.String())
	}
}

func TestPutLayout_TitleTooLong(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID,
		`{"sections":[{"id":"a","title":"`+strings.Repeat("x", MaxSectionTitleLen+1)+`","tile_ids":[]}]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "too long") {
		t.Errorf("body %q does not explain the cap", rec.Body.String())
	}
}

// TestPutLayout_TitleTrimmedOnStore checks the split between validation (which
// reports what the client got wrong) and normalization (which canonicalizes
// what it got right).
func TestPutLayout_TitleTrimmedOnStore(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[{"id":"a","title":"  Recovery  ","tile_ids":["recovery"]}]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	if got.Sections[0].Title != "Recovery" {
		t.Errorf("stored title = %q, want %q", got.Sections[0].Title, "Recovery")
	}
}

func TestPutLayout_Empty(t *testing.T) {
	r, rp, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":[]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// An empty dashboard is a real, persisted preference: Get must resolve the
	// stored row (not ErrLayoutNotFound) and return the one-empty-section floor.
	got, err := rp.layout.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("layout Get: %v", err)
	}
	assertSections(t, got.Sections, []Section{{ID: "s1", TileIDs: []TileID{}}})
}

// TestPutLayout_NoRecognizedField rejects a body carrying neither field rather
// than reading it as "empty layout" — silently wiping a dashboard on a
// malformed-but-parseable body is the wrong failure mode.
func TestPutLayout_NoRecognizedField(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"layout":["running"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutLayout_MalformedJSON(t *testing.T) {
	r, _, userID := newTestEnv(t)

	rec := putLayout(t, r, userID, `{"sections":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutLayout_NoAuth(t *testing.T) {
	r, _, _ := newTestEnv(t)

	// No auth context → the handler treats a missing user as a server fault
	// (auth middleware not applied), mirroring the summary handler.
	rec := putLayout(t, r, "", `{"sections":[{"id":"a","tile_ids":["running"]}]}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}
