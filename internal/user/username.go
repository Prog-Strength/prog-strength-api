package user

import (
	"regexp"
	"strings"
)

// Username length bounds. The regex below encodes the same 3–30 window
// (1 leading letter + 2..29 trailing chars); the consts are exported for the
// handler/clients that want to surface the limits without parsing the pattern.
const (
	UsernameMinLen = 3
	UsernameMaxLen = 30
)

// usernamePattern enforces the canonical handle shape: a leading ASCII letter
// followed by 2–29 letters/digits/underscores (3–30 chars total). Input is
// lowercased before matching, so the pattern is intentionally lowercase-only.
var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,29}$`)

// reservedUsernames is the denylist of handles that are structurally valid but
// reserved for routes, system concepts, or impersonation safety. Checked after
// charset validation so a reserved-but-valid handle reports ErrUsernameReserved
// rather than ErrUsernameInvalid. Keys are canonical (lowercased) form.
var reservedUsernames = map[string]bool{
	"me":            true,
	"api":           true,
	"admin":         true,
	"administrator": true,
	"settings":      true,
	"search":        true,
	"timeline":      true,
	"profile":       true,
	"profiles":      true,
	"user":          true,
	"users":         true,
	"support":       true,
	"prog":          true,
	"progstrength":  true,
	"prog-strength": true,
	"follow":        true,
	"follows":       true,
	"followers":     true,
	"following":     true,
	"login":         true,
	"logout":        true,
	"signin":        true,
	"signout":       true,
	"auth":          true,
	"oauth":         true,
	"health":        true,
	"healthz":       true,
	"metrics":       true,
	"about":         true,
	"help":          true,
	"terms":         true,
	"privacy":       true,
	"root":          true,
	"null":          true,
	"undefined":     true,
}

// ValidateUsername canonicalizes and validates a user-supplied handle. It
// lowercases the input (stripping a single leading "@" if present), enforces
// the charset/length/shape via usernamePattern, then rejects reserved names.
// On success it returns the canonical (lowercased) form to persist. Errors are
// the typed sentinels ErrUsernameInvalid / ErrUsernameReserved so the handler
// can map both to 400 without string-matching.
func ValidateUsername(raw string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(raw))
	canonical = strings.TrimPrefix(canonical, "@")

	if !usernamePattern.MatchString(canonical) {
		return "", ErrUsernameInvalid
	}
	if reservedUsernames[canonical] {
		return "", ErrUsernameReserved
	}
	return canonical, nil
}
