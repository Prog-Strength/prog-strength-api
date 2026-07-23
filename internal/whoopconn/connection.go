// Package whoopconn persists a user's opt-in Whoop connection: the encrypted
// Whoop OAuth access+refresh token pair plus connection metadata, one row per
// user in user_whoop_connection. Token material is stored encrypted (see
// internal/tokencrypt); this package never decrypts — it stores and returns
// the ciphertext/nonce blobs as-is. OAuth handlers and Whoop API calls live in
// later tasks.
package whoopconn

import "time"

// Status is the lifecycle of a Whoop connection.
type Status string

const (
	// StatusConnected means a usable token pair is on file.
	StatusConnected Status = "connected"
	// StatusRevoked means the user (or Whoop) revoked access; the row is
	// retained for bookkeeping but the tokens have been wiped.
	StatusRevoked Status = "revoked"
	// StatusError means a token refresh or API call failed in a way that needs
	// re-authorization; the row is retained.
	StatusError Status = "error"
)

// Connection is the metadata view of a user's Whoop connection. It
// deliberately carries no token material — read the encrypted tokens via
// Repository.GetTokens only when a call actually needs them.
type Connection struct {
	UserID         string
	WhoopUserID    int64
	Scopes         string
	Status         Status
	TokenExpiresAt time.Time
	ConnectedAt    time.Time
	UpdatedAt      time.Time
}

// TokenBundle is the encrypted access+refresh token pair stored/read together.
type TokenBundle struct {
	AccessTokenEnc    []byte
	AccessTokenNonce  []byte
	RefreshTokenEnc   []byte
	RefreshTokenNonce []byte
	ExpiresAt         time.Time
}
