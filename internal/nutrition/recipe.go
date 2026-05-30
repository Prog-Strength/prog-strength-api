package nutrition

import (
	"strings"
	"time"
)

// MaxRecipeComponents caps how many pantry items a single recipe can
// contain. Real recipes top out around 10-15 ingredients; 20 gives
// headroom without inviting abuse. The frontend mirrors this in the
// recipe builder UI, but the handler re-validates so a misbehaving
// client can't blow up the recipe_items table.
const MaxRecipeComponents = 20

// Recipe is a user-saved bag of pantry items composed into a single
// quick-add unit ("Standard Breakfast"). Macros are derived — nothing
// macro-shaped lives on this struct; ComputeRecipeMacros at log time
// is what turns components into frozen log-entry values.
//
// Soft delete via DeletedAt matches PantryItem. Recipe macros are
// freshly derived at every read, so an edit to a pantry item DOES
// change what a recipe reports — by design (the recipe is "this set
// of components," and component macros are the components' truth).
// Once a log entry is created against a recipe, however, the entry's
// macros are frozen and won't shift if the recipe is later edited.
type Recipe struct {
	ID         string
	UserID     string
	Name       string
	Components []RecipeItem
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// RecipeItem is one (pantry_item, quantity) pair inside a recipe.
// Position drives display order in the builder UI; the write path
// rewrites positions densely from 0 in one transaction.
type RecipeItem struct {
	ID           string
	PantryItemID string
	Quantity     float64
	Position     int
	CreatedAt    time.Time
}

// Validate checks the invariants the handler enforces before reaching
// the repo. Component-existence + cross-user ownership are enforced
// separately at the handler (since they require a catalog lookup);
// this method covers the shape-only checks.
func (r *Recipe) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return ErrNameRequired
	}
	if len(r.Components) == 0 {
		return ErrRecipeComponentsRequired
	}
	if len(r.Components) > MaxRecipeComponents {
		return ErrRecipeTooManyComponents
	}
	seen := make(map[string]bool, len(r.Components))
	for _, c := range r.Components {
		if strings.TrimSpace(c.PantryItemID) == "" {
			return ErrRecipeComponentPantryRequired
		}
		if seen[c.PantryItemID] {
			return ErrRecipeComponentDuplicate
		}
		seen[c.PantryItemID] = true
		if c.Quantity <= 0 {
			return ErrQuantityNonPositive
		}
	}
	return nil
}

// RecipeMacros is the derived per-batch macro total for a recipe.
// Computed at log time and at read time; never stored on the recipe
// row itself. Frozen onto a log entry's macro columns when the user
// logs a recipe-based consumption event.
type RecipeMacros struct {
	Calories float64
	ProteinG float64
	FatG     float64
	CarbsG   float64
}

// Scale returns the macros scaled by quantity (the log-entry
// multiplier). Half a recipe is Scale(0.5); a double batch is
// Scale(2). Pure math, no rounding — callers round at the display
// boundary if they want clean numbers.
func (m RecipeMacros) Scale(quantity float64) RecipeMacros {
	return RecipeMacros{
		Calories: m.Calories * quantity,
		ProteinG: m.ProteinG * quantity,
		FatG:     m.FatG * quantity,
		CarbsG:   m.CarbsG * quantity,
	}
}
