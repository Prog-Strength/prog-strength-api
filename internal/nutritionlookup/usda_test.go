package nutritionlookup

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// usdaNutrientJSON is the search-response nutrient row shape.
type usdaNutrientJSON struct {
	NutrientID   int     `json:"nutrientId,omitempty"`
	NutrientName string  `json:"nutrientName"`
	UnitName     string  `json:"unitName"`
	Value        float64 `json:"value"`
}

// usdaScrambledEggNutrients is a complete per-100g macro set:
// 212 kcal, 13.8 P, 16.2 F, 2.1 C.
func usdaScrambledEggNutrients() []usdaNutrientJSON {
	return []usdaNutrientJSON{
		{NutrientID: 1008, NutrientName: "Energy", UnitName: "KCAL", Value: 212.0},
		{NutrientID: 1003, NutrientName: "Protein", UnitName: "G", Value: 13.8},
		{NutrientID: 1004, NutrientName: "Total lipid (fat)", UnitName: "G", Value: 16.2},
		{NutrientID: 1005, NutrientName: "Carbohydrate, by difference", UnitName: "G", Value: 2.1},
	}
}

// newUSDAServer serves the given foods array and captures the query
// params of the last request.
func newUSDAServer(t *testing.T, foods []map[string]any, lastParams *url.Values) *USDAProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lastParams != nil {
			*lastParams = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"foods": foods}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewUSDAProvider(srv.Client(), "demo-key")
	p.BaseURL = srv.URL
	return p
}

func approxEqual(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestUSDAGenericFoodStaysPer100g(t *testing.T) {
	var params url.Values
	p := newUSDAServer(t, []map[string]any{{
		"fdcId":         9999,
		"description":   "Egg, whole, cooked, scrambled",
		"dataType":      "Survey (FNDDS)",
		"foodNutrients": usdaScrambledEggNutrients(),
	}}, &params)

	hits, err := p.Search(context.Background(), "scrambled eggs", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if got := params.Get("api_key"); got != "demo-key" {
		t.Errorf("api_key param = %q, want demo-key", got)
	}
	if got := params.Get("query"); got != "scrambled eggs" {
		t.Errorf("query param = %q, want scrambled eggs", got)
	}
	if got := params.Get("dataType"); got != "Foundation,SR Legacy,Survey (FNDDS),Branded" {
		t.Errorf("dataType param = %q", got)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.ServingDescription != "100 g" {
		t.Errorf("ServingDescription = %q, want %q", hit.ServingDescription, "100 g")
	}
	if hit.PerServing.Calories != 212.0 {
		t.Errorf("PerServing.Calories = %v, want 212", hit.PerServing.Calories)
	}
	if hit.Source != "usda" || hit.SourceID != "9999" {
		t.Errorf("Source/SourceID = %q/%q, want usda/9999", hit.Source, hit.SourceID)
	}
}

func TestUSDABrandedFoodScalesToLabelServing(t *testing.T) {
	p := newUSDAServer(t, []map[string]any{{
		"fdcId":                    4242,
		"description":              "Greek Yogurt, Plain",
		"dataType":                 "Branded",
		"brandOwner":               "Fage",
		"servingSize":              170.0,
		"servingSizeUnit":          "g",
		"householdServingFullText": "1 container",
		"foodNutrients":            usdaScrambledEggNutrients(),
	}}, nil)

	hits, err := p.Search(context.Background(), "fage greek yogurt", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.Brand != "Fage" {
		t.Errorf("Brand = %q, want Fage", hit.Brand)
	}
	if hit.ServingDescription != "1 container" {
		t.Errorf("ServingDescription = %q, want %q", hit.ServingDescription, "1 container")
	}
	// 212 kcal/100g × 1.7
	if hit.PerServing.Calories != 360.4 {
		t.Errorf("PerServing.Calories = %v, want 360.4", hit.PerServing.Calories)
	}
	if !approxEqual(hit.PerServing.ProteinG, 23.5, 0.1) {
		t.Errorf("PerServing.ProteinG = %v, want ≈23.5", hit.PerServing.ProteinG)
	}
}

func TestUSDADropsRowsMissingMacros(t *testing.T) {
	p := newUSDAServer(t, []map[string]any{
		{
			// Energy only — no protein/fat/carbs, so not loggable.
			"fdcId":       1,
			"description": "Mystery food",
			"foodNutrients": []usdaNutrientJSON{
				{NutrientID: 1008, NutrientName: "Energy", UnitName: "KCAL", Value: 100},
			},
		},
		{
			"fdcId":         9999,
			"description":   "Egg, whole, cooked, scrambled",
			"dataType":      "Survey (FNDDS)",
			"foodNutrients": usdaScrambledEggNutrients(),
		},
	}, nil)

	hits, err := p.Search(context.Background(), "eggs", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1 (incomplete row dropped)", len(hits))
	}
	if hits[0].SourceID != "9999" {
		t.Errorf("SourceID = %q, want 9999", hits[0].SourceID)
	}
}

func TestUSDAEnergyByNameFallback(t *testing.T) {
	// Foundation rows sometimes report energy only under the
	// Atwater-specific ids — matched by name prefix + KCAL unit.
	p := newUSDAServer(t, []map[string]any{{
		"fdcId":       777,
		"description": "Eggs, Grade A, Large, egg whole",
		"dataType":    "Foundation",
		"foodNutrients": []usdaNutrientJSON{
			{NutrientID: 2047, NutrientName: "Energy (Atwater General Factors)", UnitName: "KCAL", Value: 148.0},
			{NutrientID: 1003, NutrientName: "Protein", UnitName: "G", Value: 12.4},
			{NutrientID: 1004, NutrientName: "Total lipid (fat)", UnitName: "G", Value: 9.96},
			{NutrientID: 1005, NutrientName: "Carbohydrate, by difference", UnitName: "G", Value: 0.96},
		},
	}}, nil)

	hits, err := p.Search(context.Background(), "whole egg", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	if hits[0].PerServing.Calories != 148.0 {
		t.Errorf("PerServing.Calories = %v, want 148 (energy-by-name fallback)", hits[0].PerServing.Calories)
	}
}

func TestUSDAConfigured(t *testing.T) {
	if NewUSDAProvider(http.DefaultClient, "").Configured() {
		t.Error("Configured() = true with empty api key, want false")
	}
	if !NewUSDAProvider(http.DefaultClient, "demo-key").Configured() {
		t.Error("Configured() = false with api key set, want true")
	}
}
