package bloodpressure

import "errors"

var (
	// ErrNotFound is returned when an entry does not exist, was
	// soft-deleted, or belongs to a different user. Cross-user lookups
	// surface as ErrNotFound (not Forbidden) so a probing client can't
	// distinguish "exists for someone else" from "does not exist."
	ErrNotFound = errors.New("bloodpressure: not found")

	// ErrSystolicRange is returned for a systolic outside 50..300. Schema
	// CHECK enforces this as well; the handler-side validation gives the
	// user a clean 400.
	ErrSystolicRange = errors.New("bloodpressure: systolic must be between 50 and 300")

	// ErrDiastolicRange is returned for a diastolic outside 30..200. Same
	// rationale as ErrSystolicRange — surface as 400, not 500.
	ErrDiastolicRange = errors.New("bloodpressure: diastolic must be between 30 and 200")

	// ErrPulseRange is returned for a supplied pulse outside 20..250. Only
	// checked when a pulse is present; a nil pulse is valid.
	ErrPulseRange = errors.New("bloodpressure: pulse must be between 20 and 250")

	// ErrSystolicNotAboveDiastolic is returned when systolic <= diastolic,
	// which is physiologically impossible and usually a swapped pair.
	ErrSystolicNotAboveDiastolic = errors.New("bloodpressure: systolic must be greater than diastolic")

	// ErrMeasuredAtRequired is returned when measured_at is the zero time.
	// The handler accepts an optional client-supplied value and defaults to
	// now when missing, so callers should never hit this.
	ErrMeasuredAtRequired = errors.New("bloodpressure: measured_at is required")
)
