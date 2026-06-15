package calendarconn

import "errors"

// ErrNotFound is returned when a user has no calendar connection row. Get,
// GetRefreshToken, SetStatus, and Delete all surface it for an absent user.
var ErrNotFound = errors.New("calendarconn: not found")
