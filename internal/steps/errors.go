package steps

import "errors"

var (
	// ErrNotFound is returned when an entry does not exist or belongs to
	// a different user. Cross-user lookups surface as ErrNotFound (not
	// Forbidden) so a probing client can't distinguish "exists for
	// someone else" from "does not exist."
	ErrNotFound = errors.New("steps: not found")

	// ErrStepsOutOfRange is returned for a step count below 0 or above
	// MaxSteps. Schema CHECK enforces this as well; the handler-side
	// validation gives the user a clean 400.
	ErrStepsOutOfRange = errors.New("steps: steps must be between 0 and 200000")

	// ErrInvalidDate is returned when the {date} path segment doesn't
	// parse as YYYY-MM-DD. Surfaced as a 400.
	ErrInvalidDate = errors.New("steps: date must be YYYY-MM-DD")

	// ErrDateTooFarInFuture is returned when {date} is more than one day
	// ahead of the current UTC date. One day of slack tolerates timezone
	// midnight crossings; anything further is a typo. Surfaced as a 400.
	ErrDateTooFarInFuture = errors.New("steps: date must not be more than one day in the future")

	// ErrGoalOutOfRange is returned for a goal at or below 0 or above
	// MaxGoal. Schema CHECK enforces this as well; the handler-side
	// validation gives the user a clean 400.
	ErrGoalOutOfRange = errors.New("steps: goal must be between 1 and 200000")
)
