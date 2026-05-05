package workout

import (
	"errors"
	"fmt"
)

var (
	ErrUserIDRequired      = errors.New("workout: user ID is required")
	ErrPerformedAtRequired = errors.New("workout: performed at is required")
	ErrExercisesRequired   = errors.New("workout: at least one exercise is required")
	ErrExerciseIDRequired  = errors.New("workout: exercise ID is required")
	ErrInvalidOrder        = errors.New("workout: order must be non-negative")
	ErrSetsRequired        = errors.New("workout: at least one set is required")
	ErrInvalidReps         = errors.New("workout: reps must be positive")
	ErrInvalidWeight       = errors.New("workout: weight must be non-negative")
	ErrNotFound            = errors.New("workout: not found")
)

type InvalidEnumError struct {
	Field string
	Value string
}

func (e *InvalidEnumError) Error() string {
	return fmt.Sprintf("workout: invalid %s %q", e.Field, e.Value)
}
