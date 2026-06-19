package dashboard

import (
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
)

func TestBuildNutrition_EmptyReturnsNil(t *testing.T) {
	if got := buildNutrition(nil, nutrition.MacroGoals{}); got != nil {
		t.Errorf("no daily row should be nil, got %+v", got)
	}
}

func TestBuildNutrition_TodayAndNoGoals(t *testing.T) {
	today := []nutrition.DailyMacros{{
		Date:     "2026-06-17",
		Calories: 2100.5,
		ProteinG: 150,
		FatG:     70,
		CarbsG:   200,
	}}
	got := buildNutrition(today, nutrition.MacroGoals{})
	if got == nil {
		t.Fatal("expected section")
	}
	if got.Today.Calories != 2100.5 || got.Today.ProteinG != 150 ||
		got.Today.FatG != 70 || got.Today.CarbsG != 200 {
		t.Errorf("today macros mismatch: %+v", got.Today)
	}
	if got.Goals != nil {
		t.Errorf("goals should be nil when unset, got %+v", got.Goals)
	}
}

func TestBuildNutrition_GoalsSet(t *testing.T) {
	today := []nutrition.DailyMacros{{Date: "2026-06-17", Calories: 100}}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	goals := nutrition.MacroGoals{
		Calories: 2500, ProteinG: 180, CarbsG: 250, FatG: 80, CreatedAt: &ts,
	}
	got := buildNutrition(today, goals)
	if got.Goals == nil {
		t.Fatal("expected goals")
	}
	if got.Goals.Calories != 2500 || got.Goals.ProteinG != 180 ||
		got.Goals.CarbsG != 250 || got.Goals.FatG != 80 {
		t.Errorf("goals mismatch: %+v", got.Goals)
	}
}
