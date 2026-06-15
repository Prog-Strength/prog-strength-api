package steps

import "time"

// MaxGoal is the upper bound on a daily step goal, mirrored as a CHECK in
// 019_steps.sql. Picked so a typo is caught while still admitting any
// realistic input.
const MaxGoal = 200000

// Goal is one row in user_steps_goal: a per-user singleton holding the
// target daily step count. Steps are unitless, so unlike the bodyweight
// goal there is no denormalized unit.
//
// The "never set" state is represented in the read path by the zero
// value with nil timestamps — clients lean on that to render the
// empty-state goal affordance without a 404 dance, exactly like the
// bodyweight goal.
type Goal struct {
	UserID    string
	Goal      int
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

// Validate enforces the shape invariants the schema's CHECK constraints
// also enforce. Run handler-side so the caller gets a clean 400 instead
// of a 500 from a constraint violation. First-error-wins.
func (g Goal) Validate() error {
	if g.Goal <= 0 || g.Goal > MaxGoal {
		return ErrGoalOutOfRange
	}
	return nil
}
