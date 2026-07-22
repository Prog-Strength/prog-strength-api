package whoopconn

import "errors"

// ErrNotFound is returned when a user has no Whoop connection row. Get,
// GetByWhoopUserID, GetTokens, UpdateTokens, SetStatus, and Revoke all surface
// it for an absent user.
var ErrNotFound = errors.New("whoopconn: not found")
