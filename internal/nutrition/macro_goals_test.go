package nutrition

import (
	"context"
	"testing"
	"time"
)

func TestMacroGoals_GetReturnsZeroWhenNeverSet(t *testing.T) {
	repo := NewMemoryRepository()
	g, err := repo.GetMacroGoals(context.Background(), "user-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.ProteinG != 0 || g.CarbsG != 0 || g.FatG != 0 || g.Calories != 0 {
		t.Fatalf("never-set should be all zero, got %+v", g)
	}
	if g.CreatedAt != nil || g.UpdatedAt != nil {
		t.Fatalf("never-set should have nil timestamps, got %+v / %+v",
			g.CreatedAt, g.UpdatedAt)
	}
}

func TestMacroGoals_UpsertSetsCreatedAndUpdatedOnInsert(t *testing.T) {
	repo := NewMemoryRepository()
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	saved, err := repo.UpsertMacroGoals(context.Background(), MacroGoals{
		UserID:   "user-a",
		ProteinG: 180, CarbsG: 300, FatG: 70, Calories: 2400,
	}, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved.CreatedAt == nil || !saved.CreatedAt.Equal(now) {
		t.Fatalf("created_at not set to now: %v", saved.CreatedAt)
	}
	if saved.UpdatedAt == nil || !saved.UpdatedAt.Equal(now) {
		t.Fatalf("updated_at not set to now: %v", saved.UpdatedAt)
	}
}

func TestMacroGoals_UpsertPreservesCreatedAtOnUpdate(t *testing.T) {
	repo := NewMemoryRepository()
	t0 := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	first, _ := repo.UpsertMacroGoals(context.Background(), MacroGoals{
		UserID: "user-a", ProteinG: 100, CarbsG: 200, FatG: 50, Calories: 1800,
	}, t0)
	t1 := t0.Add(2 * time.Hour)
	second, err := repo.UpsertMacroGoals(context.Background(), MacroGoals{
		UserID: "user-a", ProteinG: 200, CarbsG: 250, FatG: 60, Calories: 2200,
	}, t1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !second.CreatedAt.Equal(*first.CreatedAt) {
		t.Fatalf("created_at should be preserved across updates: first=%v second=%v",
			first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.Equal(t1) {
		t.Fatalf("updated_at should bump to t1: %v", second.UpdatedAt)
	}
	if second.ProteinG != 200 || second.Calories != 2200 {
		t.Fatalf("update should replace values: %+v", second)
	}
}

func TestValidateMacroGoalsRequest(t *testing.T) {
	i := func(v int) *int { return &v }

	cases := []struct {
		name string
		req  putMacroGoalsRequest
		want string // "" = pass
	}{
		{"all present and valid",
			putMacroGoalsRequest{ProteinG: i(180), CarbsG: i(300), FatG: i(70), Calories: i(2400)}, ""},
		{"protein missing",
			putMacroGoalsRequest{CarbsG: i(300), FatG: i(70), Calories: i(2400)},
			"protein_g is required"},
		{"calories missing",
			putMacroGoalsRequest{ProteinG: i(180), CarbsG: i(300), FatG: i(70)},
			"calories is required"},
		{"negative carbs",
			putMacroGoalsRequest{ProteinG: i(180), CarbsG: i(-1), FatG: i(70), Calories: i(2400)},
			"carbs_g must be non-negative"},
		{"protein over cap",
			putMacroGoalsRequest{ProteinG: i(MaxMacroGrams + 1), CarbsG: i(0), FatG: i(0), Calories: i(0)},
			"protein_g must be ≤ 10000"},
		{"calories over cap",
			putMacroGoalsRequest{ProteinG: i(0), CarbsG: i(0), FatG: i(0), Calories: i(MaxCalories + 1)},
			"calories must be ≤ 100000"},
		{"zeros across the board accepted",
			putMacroGoalsRequest{ProteinG: i(0), CarbsG: i(0), FatG: i(0), Calories: i(0)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateMacroGoalsRequest(tc.req)
			if got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}
