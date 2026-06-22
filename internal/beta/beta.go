// Package beta holds the closed-beta email allowlist. The OAuth gate
// consults it at login (an allowed email gets a JWT; anyone else completes
// OAuth but is bounced back with #error=beta_required), and an admin HTTP
// surface manages the list at runtime. The allowlist lives in the database
// rather than a boot-time env var so a tester can be authorized with one
// admin API call — no secret edit, no redeploy. See
// prog-strength-docs/sows/dynamic-beta-allowlist.md.
package beta

import (
	"context"
	"strings"
	"time"
)

// AllowedEmail is one row of the allowlist. AddedBy/Note are pointers so a
// SQL NULL round-trips as nil rather than the empty string.
type AllowedEmail struct {
	Email   string    `json:"email"`
	AddedAt time.Time `json:"added_at"`
	AddedBy *string   `json:"added_by"`
	Note    *string   `json:"note"`
}

// Checker is the minimal interface the auth package depends on: it only
// needs to ask whether an email is allowed past the gate. Keeping this
// separate from Repository keeps auth's dependency surface tiny and avoids
// pulling the write methods into the auth package.
type Checker interface {
	// IsAllowed reports whether the given email may receive a JWT. The
	// comparison is case- and whitespace-insensitive. An empty table means
	// the gate is disabled — every email is allowed (pre-beta / local dev).
	IsAllowed(ctx context.Context, email string) (bool, error)
}

// normalizeEmail lowercases and trims an email address so lookups are case-
// and whitespace-insensitive, matching internal/user's normalization.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
