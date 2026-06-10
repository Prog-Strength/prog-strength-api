package user

import (
	"errors"
	"fmt"
)

var (
	ErrEmailRequired       = errors.New("user: email is required")
	ErrDisplayNameRequired = errors.New("user: display name is required")
	ErrDisplayNameTooLong  = fmt.Errorf("user: display name exceeds %d characters", 60)
	ErrHeightOutOfRange    = fmt.Errorf("user: height must be between %g and %g cm", 50.0, 250.0)
	ErrNotFound            = errors.New("user: not found")
	ErrEmailExists         = errors.New("user: email already exists")
)

// InvalidEnumError indicates a value outside the allowed enum set.
// It's a struct (not a sentinel) because the bad value is part of the message.
type InvalidEnumError struct {
	Field string
	Value string
}

func (e *InvalidEnumError) Error() string {
	return fmt.Sprintf("user: invalid %s %q", e.Field, e.Value)
}
