package nutrition

import (
	"context"
	"time"
)

// Repository persists pantry items and nutrition log entries.
// Implementations are in-memory (dev/test default) or SQLite (prod).
// All methods enforce ownership at the storage layer so handlers
// don't have to remember a user_id WHERE clause; passing a wrong
// user_id returns ErrNotFound rather than 200 on someone else's row.
type Repository interface {
	// --- Pantry items ----------------------------------------------

	CreatePantryItem(ctx context.Context, p *PantryItem) error

	// GetPantryItem returns the row by ID, scoped to user_id. Returns
	// ErrNotFound when the row does not exist, is soft-deleted, or
	// belongs to a different user.
	GetPantryItem(ctx context.Context, userID, id string) (*PantryItem, error)

	// ListPantryItems returns the user's pantry items sorted by name
	// ASC. Soft-deleted rows are excluded. `query` substring-matches
	// the name case-insensitively when non-empty.
	ListPantryItems(ctx context.Context, userID, query string) ([]PantryItem, error)

	UpdatePantryItem(ctx context.Context, p *PantryItem) error

	DeletePantryItem(ctx context.Context, userID, id string) error

	// --- Nutrition log entries -------------------------------------

	CreateNutritionLogEntry(ctx context.Context, e *NutritionLogEntry) error

	GetNutritionLogEntry(ctx context.Context, userID, id string) (*NutritionLogEntry, error)

	// ListNutritionLogEntries returns entries for the user, most
	// recent ConsumedAt first. since/until bound ConsumedAt (inclusive
	// of since, exclusive of until). Soft-deleted excluded.
	ListNutritionLogEntries(ctx context.Context, userID string, since, until *time.Time) ([]NutritionLogEntry, error)

	UpdateNutritionLogEntry(ctx context.Context, e *NutritionLogEntry) error

	DeleteNutritionLogEntry(ctx context.Context, userID, id string) error

	// DailyMacros aggregates non-deleted entries into per-day totals
	// for the [since, until) UTC-date range. Empty days do not appear
	// in the response — callers that need a dense series fill gaps
	// client-side.
	DailyMacros(ctx context.Context, userID string, since, until time.Time) ([]DailyMacros, error)

	// --- Recipes ---------------------------------------------------

	// CreateRecipe persists the recipe + its components inside one
	// transaction. Caller is responsible for validating that every
	// component's pantry_item_id exists and belongs to the user; the
	// repo just inserts.
	CreateRecipe(ctx context.Context, r *Recipe) error

	// GetRecipe returns the recipe with components in display order
	// (position ASC), scoped to user_id. Returns ErrNotFound when the
	// recipe doesn't exist, is soft-deleted, or belongs to another user.
	GetRecipe(ctx context.Context, userID, id string) (*Recipe, error)

	// ListRecipes returns the user's recipes sorted by name ASC, each
	// with its components in display order. Soft-deleted recipes are
	// excluded.
	ListRecipes(ctx context.Context, userID string) ([]Recipe, error)

	// UpdateRecipe replaces the recipe's name and component set in
	// one transaction: existing recipe_items are deleted, the new
	// set is inserted with positions taken from the slice index.
	UpdateRecipe(ctx context.Context, r *Recipe) error

	// DeleteRecipe soft-deletes the recipe. recipe_items rows are
	// CASCADE'd by the schema on hard delete, but since this is a
	// soft delete the components stay; future reads via the soft-
	// delete-aware queries won't see them anyway.
	DeleteRecipe(ctx context.Context, userID, id string) error

	// ComputeRecipeMacros returns the per-batch macro total for the
	// recipe — sum over components of (component.quantity ×
	// pantry_item per-serving macros). Used at log time to derive the
	// denormalized macros frozen onto a recipe-based log entry.
	//
	// Soft-deleted pantry items are still read for macro math: the
	// component's macros are what they always were; soft-deleting an
	// item only hides it from the pantry list, not from recipes that
	// already reference it.
	ComputeRecipeMacros(ctx context.Context, userID, recipeID string) (RecipeMacros, error)

	// --- Macro goals (per-user singleton) --------------------------

	// GetMacroGoals returns the user's daily macro targets. Returns a
	// zero-valued struct (all four numbers 0, both timestamps nil)
	// when the user has never written goals — the client uses that
	// state to render the empty-state ring outline. Never returns
	// ErrNotFound; "not set" is a value, not an error.
	GetMacroGoals(ctx context.Context, userID string) (MacroGoals, error)

	// UpsertMacroGoals atomically inserts-or-replaces the user's goals
	// row. Caller is responsible for range validation (handler
	// enforces ≤ MaxMacroGrams per macro, ≤ MaxCalories on calories);
	// the repo just persists what it's given. `now` sets updated_at on
	// every call and created_at on the first.
	UpsertMacroGoals(ctx context.Context, g MacroGoals, now time.Time) (MacroGoals, error)
}
