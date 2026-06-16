package user

import "github.com/jwallace145/progressive-overload-fitness-tracker/internal/user/handle"

// Username validation lives in the leaf internal/user/handle package so that
// internal/db (the username backfill migration) can reuse the exact same logic
// without forming an import cycle. These aliases keep existing call sites using
// user.ValidateUsername / user.UsernameMinLen / user.UsernameMaxLen unchanged.
const (
	UsernameMinLen = handle.UsernameMinLen
	UsernameMaxLen = handle.UsernameMaxLen
)

// ValidateUsername canonicalizes and validates a user-supplied handle. See
// handle.ValidateUsername for the full contract; this is a thin re-export.
func ValidateUsername(raw string) (string, error) { return handle.ValidateUsername(raw) }

// GenerateHandle returns a valid, best-effort-unique handle for a user. See
// handle.GenerateHandle for the full contract; this is a thin re-export so
// callers that already depend on internal/user need not import the leaf.
func GenerateHandle(displayName, userID string, exists func(string) (bool, error)) (string, error) {
	return handle.GenerateHandle(displayName, userID, exists)
}
