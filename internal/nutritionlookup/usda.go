package nutritionlookup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// USDA FoodData Central provider.
//
// Fallback (and the strongest source for generic/homemade foods —
// "two scrambled eggs", "1 cup cooked rice"). Search-response nutrient
// values are per 100 g/ml; Branded entries additionally carry a label
// serving size we scale to so per_serving means an actual serving, not
// an arbitrary 100 g, whenever the data allows.

const usdaSearchURL = "https://api.nal.usda.gov/fdc/v1/foods/search"

// Search every data type: Branded for packaged goods, Survey (FNDDS)
// for as-eaten foods ("fried chicken, fast food"), Foundation/SR Legacy
// for raw ingredients.
const usdaDataTypes = "Foundation,SR Legacy,Survey (FNDDS),Branded"

// FDC nutrient numbers for the four macros. Energy appears under 1008
// (kcal) on most rows and under the Atwater-specific ids on some
// Foundation rows; extractUSDAMacros falls back by name for those.
const (
	usdaNutrientEnergy  = 1008
	usdaNutrientProtein = 1003
	usdaNutrientFat     = 1004
	usdaNutrientCarbs   = 1005
)

var (
	usdaGramUnits = map[string]bool{"g": true, "grm": true, "gram": true, "grams": true}
	usdaMLUnits   = map[string]bool{"ml": true, "mlt": true}
)

// Compile-time check that *USDAProvider satisfies Provider.
var _ Provider = (*USDAProvider)(nil)

type USDAProvider struct {
	client *http.Client
	apiKey string

	// BaseURL defaults to the production FDC search endpoint; tests
	// point it at an httptest server.
	BaseURL string
}

func NewUSDAProvider(client *http.Client, apiKey string) *USDAProvider {
	return &USDAProvider{client: client, apiKey: apiKey, BaseURL: usdaSearchURL}
}

func (p *USDAProvider) Source() string   { return "usda" }
func (p *USDAProvider) Configured() bool { return p.apiKey != "" }

func (p *USDAProvider) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	params := url.Values{
		"api_key":  {p.apiKey},
		"query":    {query},
		"dataType": {usdaDataTypes},
		"pageSize": {strconv.Itoa(limit)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usda search: unexpected status %d", resp.StatusCode)
	}

	var payload struct {
		Foods []usdaFood `json:"foods"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("usda search: decode response: %w", err)
	}

	out := make([]Candidate, 0, len(payload.Foods))
	for _, food := range payload.Foods {
		c, ok := usdaToCandidate(food)
		if !ok {
			continue
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

type usdaFood struct {
	FdcID                    json.Number    `json:"fdcId"`
	Description              string         `json:"description"`
	BrandOwner               string         `json:"brandOwner"`
	BrandName                string         `json:"brandName"`
	ServingSize              float64        `json:"servingSize"`
	ServingSizeUnit          string         `json:"servingSizeUnit"`
	HouseholdServingFullText string         `json:"householdServingFullText"`
	FoodNutrients            []usdaNutrient `json:"foodNutrients"`
}

type usdaNutrient struct {
	NutrientID   int      `json:"nutrientId"`
	NutrientName string   `json:"nutrientName"`
	UnitName     string   `json:"unitName"`
	Value        *float64 `json:"value"`
}

// usdaToCandidate converts one search-response row, scaling per-100g
// nutrients to the label serving when the row carries a usable
// gram/ml serving size (Branded rows). Returns ok=false when the row
// is missing protein/fat/carbs — a candidate without them isn't
// loggable.
func usdaToCandidate(food usdaFood) (Candidate, bool) {
	per100, ok := extractUSDAMacros(food.FoodNutrients)
	if !ok {
		return Candidate{}, false
	}

	scale := 1.0
	servingDescription := "100 g"
	unit := strings.ToLower(food.ServingSizeUnit)
	if food.ServingSize > 0 && (usdaGramUnits[unit] || usdaMLUnits[unit]) {
		scale = food.ServingSize / 100.0
		unitLabel := "g"
		if usdaMLUnits[unit] {
			unitLabel = "ml"
		}
		servingDescription = strings.TrimSpace(food.HouseholdServingFullText)
		if servingDescription == "" {
			servingDescription = fmt.Sprintf("%g %s", food.ServingSize, unitLabel)
		}
	}

	brand := food.BrandOwner
	if brand == "" {
		brand = food.BrandName
	}
	return newCandidate(
		food.Description, brand, servingDescription,
		Macros{
			Calories: per100.Calories * scale,
			ProteinG: per100.ProteinG * scale,
			FatG:     per100.FatG * scale,
			CarbsG:   per100.CarbsG * scale,
		},
		"usda", food.FdcID.String(),
	), true
}

// extractUSDAMacros pulls the four macros (per 100 g) out of a
// search-response nutrient list. Returns ok=false when any macro other
// than energy is missing; energy absent is fine — it's derived from
// the Atwater factors (4/4/9).
func extractUSDAMacros(nutrients []usdaNutrient) (Macros, bool) {
	found := map[string]float64{}
	for _, n := range nutrients {
		var key string
		switch n.NutrientID {
		case usdaNutrientEnergy:
			key = "calories"
		case usdaNutrientProtein:
			key = "protein"
		case usdaNutrientFat:
			key = "fat"
		case usdaNutrientCarbs:
			key = "carbs"
		default:
			// Foundation rows sometimes report energy only under the
			// Atwater-specific ids; match those by name + unit.
			if strings.HasPrefix(strings.ToLower(n.NutrientName), "energy") && strings.ToLower(n.UnitName) == "kcal" {
				key = "calories"
			} else {
				continue
			}
		}
		if n.Value == nil {
			continue
		}
		if _, dup := found[key]; !dup {
			found[key] = *n.Value
		}
	}

	protein, hasP := found["protein"]
	fat, hasF := found["fat"]
	carbs, hasC := found["carbs"]
	if !hasP || !hasF || !hasC {
		return Macros{}, false
	}
	calories, hasE := found["calories"]
	if !hasE {
		calories = 4*protein + 4*carbs + 9*fat
	}
	return Macros{Calories: calories, ProteinG: protein, FatG: fat, CarbsG: carbs}, true
}
