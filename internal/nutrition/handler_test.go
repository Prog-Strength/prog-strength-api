package nutrition

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// logEnvelope mirrors the httpresp success shape for single-entry
// responses (POST /custom, PUT /{id}) so handler tests can assert on
// the returned entry.
type logEnvelope struct {
	Message string      `json:"message"`
	Data    logEntryDTO `json:"data"`
}

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

// seedCustomLogEntry inserts a custom-typed entry (CustomMealName set,
// quantity 1) for user u1 and returns its ID.
func seedCustomLogEntry(t *testing.T, repo *MemoryRepository, ctx context.Context, name string, calories float64) string {
	t.Helper()
	n := name
	e := &NutritionLogEntry{
		UserID: "u1", ConsumedAt: time.Now().UTC(),
		CustomMealName: &n, Quantity: 1,
		Calories: calories,
		Meal:     MealLunch,
	}
	if err := repo.CreateNutritionLogEntry(ctx, e); err != nil {
		t.Fatalf("seed custom: %v", err)
	}
	return e.ID
}

// seedRecipeLogEntry inserts a recipe-backed entry for user u1 and
// returns its ID. The recipe ID need not resolve: the PUT tests that use
// it 400 on the custom-only field guard before any recipe lookup.
func seedRecipeLogEntry(t *testing.T, repo *MemoryRepository, ctx context.Context) string {
	t.Helper()
	recipeID := "r1"
	e := &NutritionLogEntry{
		UserID: "u1", ConsumedAt: time.Now().UTC(),
		RecipeID: &recipeID, Quantity: 1,
		Calories: 500,
		Meal:     MealDinner,
	}
	if err := repo.CreateNutritionLogEntry(ctx, e); err != nil {
		t.Fatalf("seed recipe: %v", err)
	}
	return e.ID
}

// postCustom drives the createCustomLogEntry handler with the given JSON
// body and userID-in-context.
func postCustom(t *testing.T, repo *MemoryRepository, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/nutrition-log/custom", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	NewHandler(repo).createCustomLogEntry(w, req)
	return w
}

// putLog drives the updateLogEntry handler against entry id with the
// given JSON body and userID-in-context.
func putLog(t *testing.T, repo *MemoryRepository, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/nutrition-log/"+id, strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	w := httptest.NewRecorder()
	NewHandler(repo).updateLogEntry(w, req)
	return w
}

// decodeEntry asserts a 2xx and returns the single-entry payload.
func decodeEntry(t *testing.T, w *httptest.ResponseRecorder, wantStatus int) logEntryDTO {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status: got %d want %d, body=%s", w.Code, wantStatus, w.Body.String())
	}
	var got logEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Data
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

func TestCreateCustomLogEntry_HappyPath(t *testing.T) {
	repo := NewMemoryRepository()
	body := `{"name":"  Chipotle bowl  ","calories":850,"protein_g":45,"fat_g":30,"carbs_g":80,"meal":"lunch"}`
	w := postCustom(t, repo, body)
	got := decodeEntry(t, w, http.StatusCreated)

	if got.CustomMealName == nil || *got.CustomMealName != "Chipotle bowl" {
		t.Fatalf("custom_meal_name = %v, want %q", got.CustomMealName, "Chipotle bowl")
	}
	if got.Calories != 850 || got.ProteinG != 45 || got.FatG != 30 || got.CarbsG != 80 {
		t.Fatalf("macros not stored as-typed: %+v", got)
	}
	if got.Quantity != 1 {
		t.Fatalf("quantity = %v, want 1", got.Quantity)
	}
	if got.Meal != "lunch" {
		t.Fatalf("meal = %q, want lunch", got.Meal)
	}
	if got.PantryItemID != nil || got.RecipeID != nil {
		t.Fatalf("expected no pantry/recipe source, got %+v", got)
	}
}

func TestCreateCustomLogEntry_DefaultsConsumedAtToNow(t *testing.T) {
	repo := NewMemoryRepository()
	before := time.Now().UTC().Add(-time.Minute)
	w := postCustom(t, repo, `{"name":"Snack","calories":100,"protein_g":1,"fat_g":2,"carbs_g":3,"meal":"snack"}`)
	got := decodeEntry(t, w, http.StatusCreated)
	if got.ConsumedAt.IsZero() {
		t.Fatal("consumed_at is zero; expected a defaulted recent timestamp")
	}
	if got.ConsumedAt.Before(before) || got.ConsumedAt.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("consumed_at = %v, want ~now", got.ConsumedAt)
	}
}

func TestCreateCustomLogEntry_Validation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"blank name", `{"name":"   ","calories":1,"protein_g":1,"fat_g":1,"carbs_g":1,"meal":"lunch"}`, "name is required"},
		{"long name", `{"name":"` + strings.Repeat("a", 201) + `","calories":1,"protein_g":1,"fat_g":1,"carbs_g":1,"meal":"lunch"}`, "name is too long"},
		{"calories high", `{"name":"X","calories":100001,"protein_g":1,"fat_g":1,"carbs_g":1,"meal":"lunch"}`, "calories out of range"},
		{"calories negative", `{"name":"X","calories":-1,"protein_g":1,"fat_g":1,"carbs_g":1,"meal":"lunch"}`, "calories out of range"},
		{"protein high", `{"name":"X","calories":1,"protein_g":10001,"fat_g":1,"carbs_g":1,"meal":"lunch"}`, "protein_g out of range"},
		{"fat high", `{"name":"X","calories":1,"protein_g":1,"fat_g":10001,"carbs_g":1,"meal":"lunch"}`, "fat_g out of range"},
		{"carbs high", `{"name":"X","calories":1,"protein_g":1,"fat_g":1,"carbs_g":10001,"meal":"lunch"}`, "carbs_g out of range"},
		{"invalid meal", `{"name":"X","calories":1,"protein_g":1,"fat_g":1,"carbs_g":1,"meal":"brunch"}`, ErrInvalidMeal.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postCustom(t, NewMemoryRepository(), tc.body)
			assertBadRequest(t, w, tc.wantMsg)
		})
	}
}

func TestUpdateLogEntry_CustomNewName(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := seedCustomLogEntry(t, repo, ctx, "Old name", 500)

	w := putLog(t, repo, id, `{"name":"  New name  "}`)
	got := decodeEntry(t, w, http.StatusOK)
	if got.CustomMealName == nil || *got.CustomMealName != "New name" {
		t.Fatalf("custom_meal_name = %v, want %q", got.CustomMealName, "New name")
	}
	if got.Calories != 500 {
		t.Fatalf("calories changed unexpectedly: %v", got.Calories)
	}
}

func TestUpdateLogEntry_CustomNewMacros(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := seedCustomLogEntry(t, repo, ctx, "Meal", 500)

	w := putLog(t, repo, id, `{"calories":640,"protein_g":50,"fat_g":20,"carbs_g":70}`)
	got := decodeEntry(t, w, http.StatusOK)
	if got.Calories != 640 || got.ProteinG != 50 || got.FatG != 20 || got.CarbsG != 70 {
		t.Fatalf("macros not updated: %+v", got)
	}
	if got.CustomMealName == nil || *got.CustomMealName != "Meal" {
		t.Fatalf("custom_meal_name should be preserved, got %v", got.CustomMealName)
	}
}

func TestUpdateLogEntry_PantryWithNameRejected(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := seedLogEntry(t, repo, ctx, time.Now().UTC(), 200)

	w := putLog(t, repo, id, `{"name":"Whatever"}`)
	assertBadRequest(t, w, "name is only editable on custom meal entries")
}

func TestUpdateLogEntry_RecipeWithCaloriesRejected(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := seedRecipeLogEntry(t, repo, ctx)

	w := putLog(t, repo, id, `{"calories":123}`)
	assertBadRequest(t, w, "calories is only editable on custom meal entries")
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
