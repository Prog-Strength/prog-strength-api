// Package nutritionlookup grounds custom-meal macros in real data: it
// fronts external nutrition databases (FatSecret Platform for restaurant
// and branded foods, USDA FoodData Central for generic/homemade foods)
// behind a durable SQLite cache and exposes an auth-gated
// GET /nutrition/lookup endpoint. Quantity math happens here in Go —
// the agent copies totals, it never multiplies. See
// prog-strength-docs/sows/custom-meal-macro-accuracy.md.
package nutritionlookup

import (
	"fmt"
	"math"
)

// Macros is the four-number shape shared by per_serving and
// total_for_quantity on a Candidate.
type Macros struct {
	Calories float64 `json:"calories"`
	ProteinG float64 `json:"protein_g"`
	FatG     float64 `json:"fat_g"`
	CarbsG   float64 `json:"carbs_g"`
}

// Candidate is one provider hit, the JSON contract the MCP
// lookup_food_nutrition tool and the agent prompt are written against.
// Providers construct candidates with per-serving values only; the
// service fills TotalForQuantity, PlausibilityWarning, and Stale at
// scale time. Cache rows store the per-serving form.
type Candidate struct {
	Name               string `json:"name"`
	Brand              string `json:"brand"`
	ServingDescription string `json:"serving_description"`
	PerServing         Macros `json:"per_serving"`
	TotalForQuantity   Macros `json:"total_for_quantity"`
	Source             string `json:"source"`
	SourceID           string `json:"source_id"`
	// PlausibilityWarning flags rows whose stated calories diverge from
	// the Atwater-derived 4P+4C+9F value — see plausibilityWarning.
	PlausibilityWarning string `json:"plausibility_warning,omitempty"`
	// Stale marks candidates served from a cache row past the freshness
	// TTL because every provider failed — resilience over purity.
	Stale bool `json:"stale,omitempty"`
}

// newCandidate normalizes one provider hit into the shared candidate
// shape. Two decimals on per-serving values: these get multiplied by
// quantity downstream, so rounding here compounds (4.75g → 4.8g is
// +0.5g at quantity=10). Totals round to 1 decimal at scale time.
func newCandidate(name, brand, servingDescription string, per Macros, source, sourceID string) Candidate {
	return Candidate{
		Name:               name,
		Brand:              brand,
		ServingDescription: servingDescription,
		PerServing: Macros{
			Calories: round2(per.Calories),
			ProteinG: round2(per.ProteinG),
			FatG:     round2(per.FatG),
			CarbsG:   round2(per.CarbsG),
		},
		Source:   source,
		SourceID: sourceID,
	}
}

// scaled returns a copy of c with TotalForQuantity computed as
// per-serving × quantity (rounded to 1 decimal) and the plausibility
// warning attached.
func scaled(c Candidate, quantity float64) Candidate {
	c.TotalForQuantity = Macros{
		Calories: round1(c.PerServing.Calories * quantity),
		ProteinG: round1(c.PerServing.ProteinG * quantity),
		FatG:     round1(c.PerServing.FatG * quantity),
		CarbsG:   round1(c.PerServing.CarbsG * quantity),
	}
	c.PlausibilityWarning = plausibilityWarning(c.PerServing)
	return c
}

// plausibilityWarning flags macro rows whose calories diverge from the
// Atwater-derived value (4·protein + 4·carbs + 9·fat) by more than 25%.
//
// Some source entries have data-entry errors; the agent is told to
// prefer candidates without a warning. This is a heuristic, not a
// validator — fiber, sugar alcohols, and alcohol (7 cal/g) all push
// legitimate foods off the 4/4/9 line, hence the wide band and the
// floor on tiny-calorie items where the ratio is meaningless.
func plausibilityWarning(per Macros) string {
	if per.Calories <= 20 {
		return ""
	}
	derived := 4*per.ProteinG + 4*per.CarbsG + 9*per.FatG
	if math.Abs(derived-per.Calories)/per.Calories <= 0.25 {
		return ""
	}
	return fmt.Sprintf(
		"stated calories (%g) diverge >25%% from the 4P+4C+9F-derived value (%g) — "+
			"the source entry may contain a data error; prefer a candidate without this warning.",
		per.Calories, derived,
	)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round1(v float64) float64 { return math.Round(v*10) / 10 }
