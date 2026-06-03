package nutrition

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// listEnvelope mirrors the httpresp success shape with the log-entry list
// payload typed so handler tests can assert on returned entries.
type listEnvelope struct {
	Message string        `json:"message"`
	Data    []logEntryDTO `json:"data"`
}

type dailyEnvelope struct {
	Message string           `json:"message"`
	Data    []dailyMacrosDTO `json:"data"`
}

type errEnvelope struct {
	Error string `json:"error"`
}

// seedLogEntry inserts a pantry-backed entry at consumedAt for user u1 and
// fails the test on a seed error. Returns the entry ID for assertions.
func seedLogEntry(t *testing.T, repo *MemoryRepository, ctx context.Context, consumedAt time.Time, calories float64) string {
	t.Helper()
	pantryID := "p1"
	e := &NutritionLogEntry{
		UserID: "u1", ConsumedAt: consumedAt,
		PantryItemID: &pantryID, Quantity: 1,
		Calories: calories,
		Meal:     MealBreakfast,
	}
	if err := repo.CreateNutritionLogEntry(ctx, e); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return e.ID
}

// listLog drives the listLogEntries handler with the given query string and
// userID-in-context, returning the recorder for status/body assertions.
func listLog(t *testing.T, repo *MemoryRepository, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/nutrition-log?"+query, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	NewHandler(repo).listLogEntries(w, req)
	return w
}

func TestListLogEntries_MissingTimezone(t *testing.T) {
	w := listLog(t, NewMemoryRepository(), "date=2026-05-28")
	assertBadRequest(t, w, "timezone is required")
}

func TestListLogEntries_DateAndStartDateBothSupplied(t *testing.T) {
	w := listLog(t, NewMemoryRepository(), "timezone=UTC&date=2026-05-28&start_date=2026-05-28")
	assertBadRequest(t, w, "supply either date or start_date+end_date, not both")
}

func TestListLogEntries_StartDateWithoutEndDate(t *testing.T) {
	w := listLog(t, NewMemoryRepository(), "timezone=UTC&start_date=2026-05-28")
	assertBadRequest(t, w, "end_date is required when start_date is supplied")
}

func TestListLogEntries_EndDateWithoutStartDate(t *testing.T) {
	w := listLog(t, NewMemoryRepository(), "timezone=UTC&end_date=2026-05-28")
	assertBadRequest(t, w, "start_date is required when end_date is supplied")
}

func TestListLogEntries_EndBeforeStart(t *testing.T) {
	w := listLog(t, NewMemoryRepository(), "timezone=UTC&start_date=2026-05-29&end_date=2026-05-28")
	assertBadRequest(t, w, "end_date must be on or after start_date")
}

func TestListLogEntries_NoDateSupplied(t *testing.T) {
	w := listLog(t, NewMemoryRepository(), "timezone=UTC")
	assertBadRequest(t, w, "date or start_date+end_date is required")
}

func TestListLogEntries_UnknownTimezone(t *testing.T) {
	w := listLog(t, NewMemoryRepository(), "timezone=Mars/Phobos&date=2026-05-28")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	var got errEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// loadTimezone owns the exact message; assert the stable prefix.
	if got.Error == "" || got.Error[:len("invalid timezone")] != "invalid timezone" {
		t.Fatalf("error = %q, want prefix %q", got.Error, "invalid timezone")
	}
}

func TestListLogEntries_SingleDateDenverBoundary(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	// America/Denver is UTC-6 (MDT) on 2026-05-28, so the local day
	// [2026-05-28 00:00, 2026-05-29 00:00) MDT maps to
	// [2026-05-28T06:00Z, 2026-05-29T06:00Z).
	//
	// Just inside the local day (00:30 MDT = 06:30Z) -> included.
	inEarly := time.Date(2026, 5, 28, 6, 30, 0, 0, time.UTC)
	// Late local day (23:00 MDT = next-day 05:00Z) -> included.
	inLate := time.Date(2026, 5, 29, 5, 0, 0, 0, time.UTC)
	// Before the local day starts (05:30Z = 23:30 MDT on 05-27) -> excluded.
	beforeStart := time.Date(2026, 5, 28, 5, 30, 0, 0, time.UTC)
	// At/after the local day ends (06:00Z on 05-29 = 00:00 MDT 05-29) -> excluded.
	afterEnd := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)

	wantA := seedLogEntry(t, repo, ctx, inEarly, 100)
	wantB := seedLogEntry(t, repo, ctx, inLate, 200)
	seedLogEntry(t, repo, ctx, beforeStart, 300)
	seedLogEntry(t, repo, ctx, afterEnd, 400)

	w := listLog(t, repo, "timezone=America/Denver&date=2026-05-28")
	got := decodeList(t, w)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries in Denver day, got %d: %+v", len(got), got)
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids[wantA] || !ids[wantB] {
		t.Fatalf("wrong entries returned: %+v", got)
	}
}

func TestListLogEntries_RangeInclusiveDenver(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	// Range 2026-05-28..2026-05-29 (Denver, UTC-6) covers
	// [2026-05-28T06:00Z, 2026-05-30T06:00Z).
	day28 := time.Date(2026, 5, 28, 18, 0, 0, 0, time.UTC) // 12:00 MDT 05-28
	// 23:00 MDT on the end_date (05-29) = 2026-05-30T05:00Z; inside span.
	endDayLocalNight := time.Date(2026, 5, 30, 5, 0, 0, 0, time.UTC)
	// 2026-05-30T06:00Z = 00:00 MDT 05-30; just past the span -> excluded.
	pastEnd := time.Date(2026, 5, 30, 6, 0, 0, 0, time.UTC)

	wantA := seedLogEntry(t, repo, ctx, day28, 100)
	wantB := seedLogEntry(t, repo, ctx, endDayLocalNight, 200)
	seedLogEntry(t, repo, ctx, pastEnd, 300)

	w := listLog(t, repo, "timezone=America/Denver&start_date=2026-05-28&end_date=2026-05-29")
	got := decodeList(t, w)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries across span, got %d: %+v", len(got), got)
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids[wantA] || !ids[wantB] {
		t.Fatalf("wrong entries returned: %+v", got)
	}
}

func TestListLogEntries_RegressionBridgeUTC(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	// date=2026-05-28 + timezone=UTC must select exactly the explicit
	// [2026-05-28T00:00Z, 2026-05-29T00:00Z) window.
	inStart := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)  // start inclusive
	inMid := time.Date(2026, 5, 28, 23, 59, 59, 0, time.UTC) // last second
	before := time.Date(2026, 5, 27, 23, 59, 59, 0, time.UTC)
	atEnd := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC) // end exclusive

	wantA := seedLogEntry(t, repo, ctx, inStart, 100)
	wantB := seedLogEntry(t, repo, ctx, inMid, 200)
	seedLogEntry(t, repo, ctx, before, 300)
	seedLogEntry(t, repo, ctx, atEnd, 400)

	w := listLog(t, repo, "timezone=UTC&date=2026-05-28")
	got := decodeList(t, w)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries in UTC day, got %d: %+v", len(got), got)
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids[wantA] || !ids[wantB] {
		t.Fatalf("wrong entries returned: %+v", got)
	}
}

func TestDailyMacros_HandlerGroupsByLocalDateDenver(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	// Two instants that are the same Denver local day (2026-05-28) but
	// straddle the UTC date boundary, proving local-date grouping flows
	// through the handler.
	noonLocal := time.Date(2026, 5, 28, 18, 0, 0, 0, time.UTC) // 12:00 MDT 05-28
	lateLocal := time.Date(2026, 5, 29, 5, 0, 0, 0, time.UTC)  // 23:00 MDT 05-28
	seedLogEntry(t, repo, ctx, noonLocal, 400)
	seedLogEntry(t, repo, ctx, lateLocal, 600)

	req := httptest.NewRequest("GET", "/nutrition-log/daily?timezone=America/Denver&date=2026-05-28", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	NewHandler(repo).dailyMacros(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	var got dailyEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 1 {
		t.Fatalf("expected 1 Denver-day bucket, got %d: %+v", len(got.Data), got.Data)
	}
	if got.Data[0].Date != "2026-05-28" || got.Data[0].Calories != 1000 || got.Data[0].EntryCount != 2 {
		t.Fatalf("Denver bucket wrong: %+v", got.Data[0])
	}
}

func assertBadRequest(t *testing.T, w *httptest.ResponseRecorder, wantMsg string) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
	var got errEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error != wantMsg {
		t.Fatalf("error = %q, want %q", got.Error, wantMsg)
	}
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder) []logEntryDTO {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	var got listEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Data
}
