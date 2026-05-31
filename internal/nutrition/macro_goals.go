package nutrition

import "time"

// MaxMacroGrams is the per-macro upper bound enforced by the handler
// and mirrored as a CHECK in 009_user_macro_goals.sql. Picked so a
// typo like "10000" (meant "100") is caught while still admitting any
// realistic input — a 350lb strongman force-feeding might hit 700g of
// protein/day but never 10,000.
const MaxMacroGrams = 10_000

// MaxCalories is the analogous upper bound for the calories column.
// 100,000 kcal/day is well above any plausible target; below this we
// just trust the user.
const MaxCalories = 100_000

// MacroGoals is one row in user_macro_goals: a per-user singleton
// holding the four daily targets that drive the ring charts on the
// Nutrition Today view and feed the get_macro_goals MCP tool.
//
// Set semantics are set-replacement (PUT, not PATCH per-field): the
// four numbers are conceptually one goal, so the API never accepts a
// partial update. See daily-macro-goals.md §Proposed Solution.
//
// The "never set" state is represented in the read path by a row of
// all zeros with nil timestamps — clients lean on that to render the
// empty-state ring outline without a 404 dance.
type MacroGoals struct {
	UserID    string
	ProteinG  int
	CarbsG    int
	FatG      int
	Calories  int
	CreatedAt *time.Time
	UpdatedAt *time.Time
}
