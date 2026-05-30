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
}
