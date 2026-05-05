package exercise

import (
	"errors"
	"fmt"
)

var (
	ErrNameRequired         = errors.New("exercise: name is required")
	ErrMuscleGroupsRequired = errors.New("exercise: at least one muscle group is required")
	ErrNotFound             = errors.New("exercise: not found")
)

// InvalidEnumError indicates a value outside the allowed enum set.
// It's a struct (not a sentinel) because the bad value is part of the message.
type InvalidEnumError struct {
	Field string
	Value string
}

func (e *InvalidEnumError) Error() string {
	return fmt.Sprintf("exercise: invalid %s %q", e.Field, e.Value)
}
