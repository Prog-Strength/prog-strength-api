package user

import (
	"errors"
	"fmt"
)

var (
	ErrEmailRequired         = errors.New("user: email is required")
	ErrDisplayNameRequired   = errors.New("user: display name is required")
	ErrDisplayNameTooLong    = fmt.Errorf("user: display name exceeds %d characters", 60)
	ErrHeightOutOfRange      = fmt.Errorf("user: height must be between %g and %g cm", 50.0, 250.0)
	ErrInvalidTimezone       = errors.New("user: timezone must be a valid IANA timezone")
	ErrInvalidCalendarDetail = errors.New("user: calendar_default_detail must be time_block or full_agenda")
	ErrNotFound              = errors.New("user: not found")
	ErrEmailExists           = errors.New("user: email already exists")

	// Username write-path errors. Invalid covers charset/length/shape (input
	// the regex rejects); Reserved is a structurally-valid but denylisted name;
	// Taken is a case-insensitive collision surfaced from the unique index.
	ErrUsernameInvalid  = errors.New("user: username must be 3-30 chars, start with a letter, and contain only letters, digits, and underscores")
	ErrUsernameReserved = errors.New("user: username is reserved")
	ErrUsernameTaken    = errors.New("user: username already taken")
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
