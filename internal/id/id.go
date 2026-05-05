package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New generates a random 16-byte hex ID.
// Replace with ULID/UUID when you have a real reason; this is sufficient for now.
func New() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
