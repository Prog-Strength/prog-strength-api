package dashboard

import (
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
)

// buildNutrition assembles the nutrition tile from the local-day macro
// aggregate and the user's goals. It is pure: today holds the single
// already-computed daily row (caller scopes it to the local day). Returns nil
// when there is no aggregate row at all.
func buildNutrition(today []nutrition.DailyMacros, goals nutrition.MacroGoals) *NutritionSection {
	if len(today) == 0 {
		return nil
	}

	d := today[0]
	return &NutritionSection{
		Today: NutritionMacros{
			Calories: d.Calories,
			ProteinG: d.ProteinG,
			CarbsG:   d.CarbsG,
			FatG:     d.FatG,
		},
		Goals: nutritionGoals(goals),
	}
}

// nutritionGoals returns the goal struct, or nil when unset. The read path
// represents "never set" as a row of zeros with a nil CreatedAt.
func nutritionGoals(goals nutrition.MacroGoals) *NutritionGoals {
	if goals.CreatedAt == nil {
		return nil
	}
	return &NutritionGoals{
		Calories: goals.Calories,
		ProteinG: goals.ProteinG,
		CarbsG:   goals.CarbsG,
		FatG:     goals.FatG,
	}
}
