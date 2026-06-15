// Package calendarconn persists a user's opt-in Google Calendar connection:
// the encrypted Google refresh token plus sync metadata, one row per user in
// user_calendar_connection. Token material is stored encrypted (see
// internal/calendarsync); this package never decrypts — it stores and returns
// the ciphertext/nonce blobs as-is. OAuth handlers and Google API calls live
// in later tasks.
package calendarconn

import "time"

// Status is the lifecycle of a calendar connection.
type Status string

const (
	// StatusConnected means a usable refresh token is on file.
	StatusConnected Status = "connected"
	// StatusRevoked means the user (or Google) revoked access; the row is
	// retained for bookkeeping but the token should not be used.
	StatusRevoked Status = "revoked"
)

// Connection is the metadata view of a user's calendar connection. It
// deliberately carries no token material — read the encrypted token via
// Repository.GetRefreshToken only when a sync actually needs it.
type Connection struct {
	UserID           string
	GoogleCalendarID string
	Scopes           string
	Status           Status
	ConnectedAt      time.Time
	UpdatedAt        time.Time
}
