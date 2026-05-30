// Package bodyweight tracks scale readings keyed by user + timestamp.
// See prog-strength-docs/sows/daily-nutrition-log.md (Phase 3).
//
// Bodyweight lives in its own package — not under nutrition — because
// it is conceptually independent (a scale reading is a measurement,
// not a consumption event) even though the two pair for diet
// inference. Keeping them split means the read paths don't share an
// interface and a future bodyweight-only consumer (a smart-scale
// integration, say) can use this package without pulling in nutrition.
package bodyweight

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Entry is one bodyweight measurement. Unit is denormalized per row
// so a user changing their preferred unit doesn't reinterpret history
// — 200 lb stays 200 lb regardless of UI preference shifts.
type Entry struct {
	ID         string
	UserID     string
	Weight     float64
	Unit       user.WeightUnit
	MeasuredAt time.Time
	CreatedAt  time.Time
	DeletedAt  *time.Time
}

// Validate enforces the shape invariants the schema's CHECK
// constraints also enforce. Run handler-side so the caller gets a
// clean 400 instead of a 500 from a constraint violation.
func (e *Entry) Validate() error {
	if e.Weight <= 0 {
		return ErrWeightNonPositive
	}
	if !e.Unit.Valid() {
		return ErrInvalidUnit
	}
	if e.MeasuredAt.IsZero() {
		return ErrMeasuredAtRequired
	}
	return nil
}
