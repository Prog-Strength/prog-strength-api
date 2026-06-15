// Package steps tracks daily step totals keyed by user + calendar date.
//
// Steps live in their own package — parallel to bodyweight — because
// they are conceptually independent activity data with their own read
// paths. Unlike bodyweight, steps are date-keyed and upserted: one
// cumulative total per day, so re-logging a day overwrites rather than
// appends. They are unitless and hard-deleted (no soft-delete audit
// trail) — a step count is disposable derived data, not history.
package steps

import "time"

// Entry is one day's step total. Date is the YYYY-MM-DD calendar day the
// count belongs to; the (UserID, Date) pair is unique, so the storage
// layer upserts rather than inserts. Steps are unitless.
type Entry struct {
	ID        string
	UserID    string
	Date      string
	Steps     int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MaxSteps is the upper bound on a daily step count, mirrored as a CHECK
// in 019_steps.sql. Picked so a typo is caught while still admitting any
// realistic input.
const MaxSteps = 200000

// Validate enforces the shape invariants the schema's CHECK constraints
// also enforce. Run handler-side so the caller gets a clean 400 instead
// of a 500 from a constraint violation. First-error-wins.
func (e *Entry) Validate() error {
	if e.Steps < 0 || e.Steps > MaxSteps {
		return ErrStepsOutOfRange
	}
	return nil
}
