package nutrition

import "time"

// NutritionLogEntry is one consumption event. Exactly one of
// PantryItemID or RecipeID is set; macros are denormalized so a
// future edit of a pantry item does not retroactively change last
// Tuesday's totals.
//
// Phase 1 only emits entries with PantryItemID set; RecipeID stays
// nil until the recipes domain ships. The CHECK on the underlying
// table enforces exactly-one regardless of which phase wrote the row.
type NutritionLogEntry struct {
	ID            string
	UserID        string
	ConsumedAt    time.Time
	PantryItemID  *string
	RecipeID      *string
	Quantity      float64
	Calories      float64
	ProteinG      float64
	FatG          float64
	CarbsG        float64
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

// DailyMacros is the aggregate over a single calendar day (UTC) of
// every non-deleted log entry the user has. Computed server-side by
// the repository so the frontend's daily widget and the agent's
// get_daily_macros tool share the same numbers without having to
// re-aggregate per caller.
type DailyMacros struct {
	// Date is the UTC calendar date in YYYY-MM-DD form. Keeping the
	// canonical string in the response (rather than a time.Time) lets
	// JSON consumers diff dates without time-zone gymnastics on the
	// frontend.
	Date       string
	Calories   float64
	ProteinG   float64
	FatG       float64
	CarbsG     float64
	EntryCount int
}
