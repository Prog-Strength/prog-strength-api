package nutrition

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPantryItem_ValidateRejectsBlankName(t *testing.T) {
	p := PantryItem{Name: "  ", ServingSize: 1, ServingUnit: "egg"}
	if err := p.Validate(); !errors.Is(err, ErrNameRequired) {
		t.Errorf("want ErrNameRequired, got %v", err)
	}
}

func TestPantryItem_ValidateRejectsNegativeMacros(t *testing.T) {
	p := PantryItem{
		Name: "Egg", ServingSize: 1, ServingUnit: "egg",
		Calories: -1,
	}
	if err := p.Validate(); !errors.Is(err, ErrMacrosNegative) {
		t.Errorf("want ErrMacrosNegative, got %v", err)
	}
}

func TestPantryItem_ValidateAcceptsZeroMacros(t *testing.T) {
	// Water-like items: 0 across the board is legal.
	p := PantryItem{
		Name: "Water", ServingSize: 100, ServingUnit: "ml",
	}
	if err := p.Validate(); err != nil {
		t.Errorf("zero-macro item should validate, got %v", err)
	}
}

func TestCreateGetUpdateDeletePantry_Roundtrip(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	p := &PantryItem{
		UserID:      "u1",
		Name:        "Egg",
		Calories:    70,
		ProteinG:    6,
		FatG:        5,
		CarbsG:      0.5,
		ServingSize: 1,
		ServingUnit: "egg",
	}
	if err := repo.CreatePantryItem(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == "" {
		t.Fatal("create did not populate ID")
	}

	got, err := repo.GetPantryItem(ctx, "u1", p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Egg" || got.Calories != 70 {
		t.Errorf("get returned wrong shape: %+v", got)
	}

	// Cross-user lookup returns ErrNotFound — never 200, never the
	// other user's row.
	if _, err := repo.GetPantryItem(ctx, "u2", p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user get: want ErrNotFound, got %v", err)
	}

	p.Calories = 80
	if err := repo.UpdatePantryItem(ctx, p); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = repo.GetPantryItem(ctx, "u1", p.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Calories != 80 {
		t.Errorf("update did not persist: got %v", got.Calories)
	}

	if err := repo.DeletePantryItem(ctx, "u1", p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetPantryItem(ctx, "u1", p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: want ErrNotFound, got %v", err)
	}
}

func TestListPantryItems_FiltersSubstringCaseInsensitive(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	for _, name := range []string{"Egg", "Chicken Breast", "Banana", "Bacon"} {
		if err := repo.CreatePantryItem(ctx, &PantryItem{
			UserID: "u1", Name: name,
			Calories: 100, ServingSize: 1, ServingUnit: "serving",
		}); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
	}

	got, err := repo.ListPantryItems(ctx, "u1", "ba")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Banana + Bacon match "ba" case-insensitive; Egg and Chicken do not.
	if len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(got), got)
	}
	// Sorted alphabetically by name (case-insensitive).
	if got[0].Name != "Bacon" || got[1].Name != "Banana" {
		t.Errorf("expected Bacon, Banana; got %s, %s", got[0].Name, got[1].Name)
	}
}

func TestLogEntry_ExactlyOneReferenceEnforced(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	// Neither reference set.
	err := repo.CreateNutritionLogEntry(ctx, &NutritionLogEntry{
		UserID: "u1", Quantity: 1, ConsumedAt: time.Now(),
		Meal: MealBreakfast,
	})
	if !errors.Is(err, ErrLogEntryReferenceRequired) {
		t.Errorf("neither set: want ErrLogEntryReferenceRequired, got %v", err)
	}

	// Both references set.
	pantryID := "p1"
	recipeID := "r1"
	err = repo.CreateNutritionLogEntry(ctx, &NutritionLogEntry{
		UserID: "u1", Quantity: 1, ConsumedAt: time.Now(),
		PantryItemID: &pantryID, RecipeID: &recipeID,
		Meal: MealBreakfast,
	})
	if !errors.Is(err, ErrLogEntryReferenceRequired) {
		t.Errorf("both set: want ErrLogEntryReferenceRequired, got %v", err)
	}
}

func TestMealType_ValidAcceptsFourValues(t *testing.T) {
	for _, m := range []MealType{MealBreakfast, MealLunch, MealDinner, MealSnack} {
		if !m.Valid() {
			t.Errorf("%q should be valid", m)
		}
	}
}

func TestMealType_ValidRejectsOthers(t *testing.T) {
	for _, m := range []MealType{"", "brunch", "Lunch", "supper", "elevenses"} {
		if MealType(m).Valid() {
			t.Errorf("%q should NOT be valid", m)
		}
	}
}

func TestLogEntry_RejectsInvalidMeal(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	pantryID := "p1"
	err := repo.CreateNutritionLogEntry(ctx, &NutritionLogEntry{
		UserID: "u1", Quantity: 1, ConsumedAt: time.Now(),
		PantryItemID: &pantryID,
		Meal:         "brunch",
	})
	if !errors.Is(err, ErrInvalidMeal) {
		t.Errorf("want ErrInvalidMeal, got %v", err)
	}
}

func TestLogEntry_MealRoundtrip(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	pantryID := "p1"
	entry := &NutritionLogEntry{
		UserID: "u1", Quantity: 1, ConsumedAt: time.Now(),
		PantryItemID: &pantryID,
		Meal:         MealDinner,
	}
	if err := repo.CreateNutritionLogEntry(ctx, entry); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.GetNutritionLogEntry(ctx, "u1", entry.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Meal != MealDinner {
		t.Errorf("meal roundtrip: got %q, want %q", got.Meal, MealDinner)
	}
}

func TestDailyMacros_AggregatesPerUTCDate(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	pantryID := "p1"
	mustLog := func(t *testing.T, consumedAt time.Time, cal, p, f, c float64) {
		t.Helper()
		if err := repo.CreateNutritionLogEntry(ctx, &NutritionLogEntry{
			UserID: "u1", ConsumedAt: consumedAt,
			PantryItemID: &pantryID, Quantity: 1,
			Calories: cal, ProteinG: p, FatG: f, CarbsG: c,
			Meal: MealBreakfast,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	day1 := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	day1Evening := time.Date(2026, 5, 28, 20, 30, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)

	mustLog(t, day1, 500, 30, 20, 50)
	mustLog(t, day1Evening, 700, 50, 25, 60)
	mustLog(t, day2, 400, 40, 10, 30)

	got, err := repo.DailyMacros(ctx, "u1",
		time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("daily macros: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 day buckets, got %d", len(got))
	}
	if got[0].Date != "2026-05-28" || got[0].Calories != 1200 || got[0].EntryCount != 2 {
		t.Errorf("day1 bucket wrong: %+v", got[0])
	}
	if got[1].Date != "2026-05-29" || got[1].Calories != 400 || got[1].EntryCount != 1 {
		t.Errorf("day2 bucket wrong: %+v", got[1])
	}
}

func TestDailyMacros_RangeExcludesSoftDeleted(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	pantryID := "p1"
	consumedAt := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	entry := &NutritionLogEntry{
		UserID: "u1", ConsumedAt: consumedAt,
		PantryItemID: &pantryID, Quantity: 1,
		Calories: 500,
		Meal:     MealLunch,
	}
	if err := repo.CreateNutritionLogEntry(ctx, entry); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.DeleteNutritionLogEntry(ctx, "u1", entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := repo.DailyMacros(ctx, "u1",
		time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("daily macros: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("soft-deleted entry should not contribute, got %+v", got)
	}
}
