package nutrition

// MealType is the bucket a nutrition log entry rolls up into on the
// /nutrition page's per-meal sections. Hard enum — the schema
// CHECK and the handler validation both compare against this exact
// set, so a typo on either side surfaces immediately.
//
// Order matters here only for display fallback ordering when the
// frontend hasn't been told otherwise — the frontend pins its own
// section order (Breakfast → Lunch → Dinner → Snacks), which is
// what users mentally expect a day to read like.
type MealType string

const (
	MealBreakfast MealType = "breakfast"
	MealLunch     MealType = "lunch"
	MealDinner    MealType = "dinner"
	MealSnack     MealType = "snack"
)

// Valid reports whether m is one of the four allowed meal values.
func (m MealType) Valid() bool {
	switch m {
	case MealBreakfast, MealLunch, MealDinner, MealSnack:
		return true
	}
	return false
}
