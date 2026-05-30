package bodyweight

import "errors"

var (
	// ErrNotFound is returned when an entry does not exist, was
	// soft-deleted, or belongs to a different user. Cross-user lookups
	// surface as ErrNotFound (not Forbidden) so a probing client can't
	// distinguish "exists for someone else" from "does not exist."
	ErrNotFound = errors.New("bodyweight: not found")

	// ErrWeightNonPositive is returned for weight ≤ 0. Schema CHECK
	// enforces this as well; the handler-side validation gives the
	// user a clean 400.
	ErrWeightNonPositive = errors.New("bodyweight: weight must be > 0")

	// ErrInvalidUnit is returned when unit isn't "lb" or "kg". Same
	// rationale as ErrWeightNonPositive — surface as 400, not 500.
	ErrInvalidUnit = errors.New("bodyweight: unit must be 'lb' or 'kg'")

	// ErrMeasuredAtRequired is returned when measured_at is the zero
	// time. The handler accepts an optional client-supplied value and
	// defaults to now when missing, so callers should never hit this.
	ErrMeasuredAtRequired = errors.New("bodyweight: measured_at is required")
)
