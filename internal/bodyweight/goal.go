package bodyweight

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// MaxGoalWeight is the upper bound on a bodyweight goal, mirrored as a
// CHECK in 011_user_bodyweight_goal.sql. Picked so a typo is caught
// while still admitting any realistic input.
const MaxGoalWeight = 2000

// Goal is one row in user_bodyweight_goal: a per-user singleton holding
// the target weight + unit. Unit is denormalized per row so a user
// changing their preferred unit doesn't reinterpret their goal.
//
// The "never set" state is represented in the read path by the zero
// value with nil timestamps — clients lean on that to render the
// empty-state goal affordance without a 404 dance, exactly like
// nutrition.MacroGoals.
type Goal struct {
	UserID    string
	Weight    float64
	Unit      user.WeightUnit
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

// Validate enforces the shape invariants the schema's CHECK constraints
// also enforce. Run handler-side so the caller gets a clean 400 instead
// of a 500 from a constraint violation. First-error-wins.
func (g Goal) Validate() error {
	if g.Weight <= 0 {
		return ErrWeightNonPositive
	}
	if g.Weight > MaxGoalWeight {
		return ErrWeightTooLarge
	}
	if !g.Unit.Valid() {
		return ErrInvalidUnit
	}
	return nil
}
