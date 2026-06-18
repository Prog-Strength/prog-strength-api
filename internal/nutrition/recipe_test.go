package nutrition

import (
	"context"
	"errors"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// seedPantry helper: create a pantry item and return its ID.
func seedPantry(t *testing.T, repo *SQLiteRepository, name string, cal, p, f, c float64) string {
	t.Helper()
	item := &PantryItem{
		UserID: "u1", Name: name,
		Calories: cal, ProteinG: p, FatG: f, CarbsG: c,
		ServingSize: 1, ServingUnit: "serving",
	}
	if err := repo.CreatePantryItem(context.Background(), item); err != nil {
		t.Fatalf("seed pantry %q: %v", name, err)
	}
	return item.ID
}

func TestRecipe_ValidateRejectsEmptyComponents(t *testing.T) {
	r := Recipe{Name: "Empty Plate"}
	if err := r.Validate(); !errors.Is(err, ErrRecipeComponentsRequired) {
		t.Errorf("want ErrRecipeComponentsRequired, got %v", err)
	}
}

func TestRecipe_ValidateRejectsDuplicateComponents(t *testing.T) {
	r := Recipe{
		Name: "Double Egg",
		Components: []RecipeItem{
			{PantryItemID: "p-egg", Quantity: 1},
			{PantryItemID: "p-egg", Quantity: 1},
		},
	}
	if err := r.Validate(); !errors.Is(err, ErrRecipeComponentDuplicate) {
		t.Errorf("want ErrRecipeComponentDuplicate, got %v", err)
	}
}

func TestRecipe_ValidateRejectsOverCap(t *testing.T) {
	components := make([]RecipeItem, MaxRecipeComponents+1)
	for i := range components {
		components[i] = RecipeItem{
			PantryItemID: "p-" + string(rune('a'+i)),
			Quantity:     1,
		}
	}
	r := Recipe{Name: "Too many", Components: components}
	if err := r.Validate(); !errors.Is(err, ErrRecipeTooManyComponents) {
		t.Errorf("want ErrRecipeTooManyComponents, got %v", err)
	}
}

func TestRecipeRoundtrip_PersistsComponentOrder(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	egg := seedPantry(t, repo, "Egg", 70, 6, 5, 0.5)
	bacon := seedPantry(t, repo, "Turkey Bacon", 35, 5, 1, 0)
	bagel := seedPantry(t, repo, "Bagel", 280, 11, 1, 56)

	r := &Recipe{
		UserID: "u1", Name: "Standard Breakfast",
		Components: []RecipeItem{
			{PantryItemID: egg, Quantity: 5},
			{PantryItemID: bacon, Quantity: 2},
			{PantryItemID: bagel, Quantity: 1},
		},
	}
	if err := repo.CreateRecipe(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}
	if r.ID == "" {
		t.Fatal("create did not populate ID")
	}

	got, err := repo.GetRecipe(ctx, "u1", r.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Standard Breakfast" || len(got.Components) != 3 {
		t.Errorf("get returned wrong shape: %+v", got)
	}
	// Positions should be densely 0,1,2 in component order.
	for i, c := range got.Components {
		if c.Position != i {
			t.Errorf("component %d: position %d, want %d", i, c.Position, i)
		}
	}
}

func TestRecipeMacros_SumOfScaledComponents(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	// 1 egg = 70 cal, 6P, 5F, 0.5C
	// 1 strip turkey bacon = 35 cal, 5P, 1F, 0C
	// 1 bagel = 280 cal, 11P, 1F, 56C
	egg := seedPantry(t, repo, "Egg", 70, 6, 5, 0.5)
	bacon := seedPantry(t, repo, "Turkey Bacon", 35, 5, 1, 0)
	bagel := seedPantry(t, repo, "Bagel", 280, 11, 1, 56)

	r := &Recipe{
		UserID: "u1", Name: "Standard Breakfast",
		Components: []RecipeItem{
			{PantryItemID: egg, Quantity: 5},
			{PantryItemID: bacon, Quantity: 2},
			{PantryItemID: bagel, Quantity: 1},
		},
	}
	if err := repo.CreateRecipe(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.ComputeRecipeMacros(ctx, "u1", r.ID)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	// 5×70 + 2×35 + 1×280 = 350 + 70 + 280 = 700 cal
	// 5×6 + 2×5 + 1×11 = 30 + 10 + 11 = 51 P
	// 5×5 + 2×1 + 1×1 = 25 + 2 + 1 = 28 F
	// 5×0.5 + 2×0 + 1×56 = 2.5 + 0 + 56 = 58.5 C
	if got.Calories != 700 {
		t.Errorf("calories: got %v, want 700", got.Calories)
	}
	if got.ProteinG != 51 {
		t.Errorf("protein: got %v, want 51", got.ProteinG)
	}
	if got.FatG != 28 {
		t.Errorf("fat: got %v, want 28", got.FatG)
	}
	if got.CarbsG != 58.5 {
		t.Errorf("carbs: got %v, want 58.5", got.CarbsG)
	}
}

func TestRecipeMacros_ScaleAppliesQuantity(t *testing.T) {
	m := RecipeMacros{Calories: 700, ProteinG: 51, FatG: 28, CarbsG: 58.5}
	doubled := m.Scale(2)
	if doubled.Calories != 1400 || doubled.ProteinG != 102 || doubled.FatG != 56 || doubled.CarbsG != 117 {
		t.Errorf("Scale(2) wrong: %+v", doubled)
	}
	half := m.Scale(0.5)
	if half.Calories != 350 {
		t.Errorf("Scale(0.5) calories: got %v, want 350", half.Calories)
	}
}

func TestRecipeMacros_RejectsCrossUserAccess(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	egg := seedPantry(t, repo, "Egg", 70, 6, 5, 0.5)
	r := &Recipe{
		UserID: "u1", Name: "Eggs",
		Components: []RecipeItem{{PantryItemID: egg, Quantity: 2}},
	}
	if err := repo.CreateRecipe(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := repo.ComputeRecipeMacros(ctx, "u2", r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user compute: want ErrNotFound, got %v", err)
	}
}

func TestRecipeUpdate_ReplacesComponentSet(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	egg := seedPantry(t, repo, "Egg", 70, 6, 5, 0.5)
	bacon := seedPantry(t, repo, "Turkey Bacon", 35, 5, 1, 0)
	bagel := seedPantry(t, repo, "Bagel", 280, 11, 1, 56)

	r := &Recipe{
		UserID: "u1", Name: "Breakfast",
		Components: []RecipeItem{
			{PantryItemID: egg, Quantity: 5},
			{PantryItemID: bacon, Quantity: 2},
		},
	}
	if err := repo.CreateRecipe(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Swap to an entirely different component set + new name.
	r.Name = "Carbs Day"
	r.Components = []RecipeItem{
		{PantryItemID: bagel, Quantity: 2},
	}
	if err := repo.UpdateRecipe(ctx, r); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.GetRecipe(ctx, "u1", r.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Name != "Carbs Day" || len(got.Components) != 1 {
		t.Errorf("update did not replace cleanly: %+v", got)
	}
	if got.Components[0].PantryItemID != bagel {
		t.Errorf("wrong component after update: %v", got.Components[0])
	}
}

func TestRecipeDelete_HidesFromListAndCompute(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	egg := seedPantry(t, repo, "Egg", 70, 6, 5, 0.5)
	r := &Recipe{
		UserID: "u1", Name: "Eggs",
		Components: []RecipeItem{{PantryItemID: egg, Quantity: 2}},
	}
	if err := repo.CreateRecipe(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.DeleteRecipe(ctx, "u1", r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetRecipe(ctx, "u1", r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: want ErrNotFound, got %v", err)
	}
	if _, err := repo.ComputeRecipeMacros(ctx, "u1", r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("compute after delete: want ErrNotFound, got %v", err)
	}
	got, err := repo.ListRecipes(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("list after delete should be empty, got %d", len(got))
	}
}

func TestRecipeMacros_SoftDeletedComponentStillContributes(t *testing.T) {
	// A pantry item that the user later removed from their pantry list
	// (soft delete) should still contribute to recipes that already
	// reference it — the recipe's component math doesn't shift just
	// because the user cleaned up their pantry view.
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	egg := seedPantry(t, repo, "Egg", 70, 6, 5, 0.5)
	r := &Recipe{
		UserID: "u1", Name: "Eggs",
		Components: []RecipeItem{{PantryItemID: egg, Quantity: 2}},
	}
	if err := repo.CreateRecipe(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.DeletePantryItem(ctx, "u1", egg); err != nil {
		t.Fatalf("delete pantry: %v", err)
	}
	got, err := repo.ComputeRecipeMacros(ctx, "u1", r.ID)
	if err != nil {
		t.Fatalf("compute after pantry delete: %v", err)
	}
	if got.Calories != 140 {
		t.Errorf("expected soft-deleted component to still contribute (2 × 70 = 140), got %v", got.Calories)
	}
}
