package bloodpressure

import "time"

// Entry is one blood-pressure measurement. systolic/diastolic are
// required; Pulse is optional (some cuffs don't report it) and stays a
// pointer so "not measured" is distinct from a real zero. Category is
// derived, not stored — it's a pure function of the two pressures.
type Entry struct {
	ID         string
	UserID     string
	Systolic   int
	Diastolic  int
	Pulse      *int
	MeasuredAt time.Time
	CreatedAt  time.Time
	DeletedAt  *time.Time
}

// Category returns the reading's clinical classification. Derived on read
// so it always tracks the current Classify rules rather than a value
// frozen at insert time.
func (e *Entry) Category() Category { return Classify(e.Systolic, e.Diastolic) }

// Validate enforces the shape invariants the schema's CHECK constraints
// also enforce. Run handler-side so the caller gets a clean 400 instead
// of a 500 from a constraint violation. Each invariant maps to a distinct
// error so the message names the field the caller got wrong; the order is
// first-error-wins, matching this repo's validation convention.
func (e *Entry) Validate() error {
	if e.Systolic < 50 || e.Systolic > 300 {
		return ErrSystolicRange
	}
	if e.Diastolic < 30 || e.Diastolic > 200 {
		return ErrDiastolicRange
	}
	if e.Systolic <= e.Diastolic {
		return ErrSystolicNotAboveDiastolic
	}
	if e.Pulse != nil && (*e.Pulse < 20 || *e.Pulse > 250) {
		return ErrPulseRange
	}
	if e.MeasuredAt.IsZero() {
		return ErrMeasuredAtRequired
	}
	return nil
}
