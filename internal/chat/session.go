package chat

import (
	"strings"
	"time"
)

// MaxTitleLen mirrors the 80-character cap the PATCH endpoint
// enforces. Lifted to a constant so the agent's /title response
// formatter can use the same value when it truncates Haiku's
// occasional over-long outputs.
const MaxTitleLen = 80

// Session is one persistent chat conversation between a user and the
// agent. Title is empty until the LLM-titling roundtrip lands; the
// frontend renders a placeholder during that ~1s gap.
//
// LastMessageAt drives the history-list sort and the eviction
// picker. It's bumped server-side on every message append, so the
// repo is the only writer.
type Session struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	Title         string     `json:"title"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	LastMessageAt time.Time  `json:"last_message_at"`
	DeletedAt     *time.Time `json:"-"`

	// LastIntent is the most recent non-general intent classification
	// emitted by the agent's router for this conversation. Nil for
	// sessions with no classified intent yet (fresh sessions, or only
	// general turns so far). Read by the agent before classifying the
	// next turn; written by the telemetry POST when the agent finishes
	// a non-general turn. JSON-hidden; surfaced via the dedicated
	// /internal/chat-sessions/{id}/intent endpoint instead of the
	// regular session payload.
	LastIntent   *string    `json:"-"`
	LastIntentAt *time.Time `json:"-"`
}

// IdleSession is the minimal session identity the vectormemory
// distillation job needs to pull a transcript and stamp progress: the
// session id and its owning user. The job runs cross-user (no caller
// user in hand), so it carries the user_id forward to DistillSession
// rather than re-deriving it.
type IdleSession struct {
	ID     string
	UserID string
}

// ValidateForCreate runs on the input the handler builds from the
// authed user + the client's POST body. UserID + ID must be set;
// anything else is the repo's responsibility to fill.
func (s *Session) ValidateForCreate() error {
	if s.UserID == "" {
		return ErrUserIDRequired
	}
	if s.ID == "" {
		return ErrSessionIDRequired
	}
	if !looksLikeUUID(s.ID) {
		return ErrInvalidSessionID
	}
	return nil
}

// NormalizeTitle trims whitespace and applies the length cap so the
// repo's stored value is exactly what callers see on read. Returns
// ErrTitleLength when the input is empty or too long *after*
// trimming — the handler maps that to 400.
func NormalizeTitle(title string) (string, error) {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return "", ErrTitleLength
	}
	if len(trimmed) > MaxTitleLen {
		return "", ErrTitleLength
	}
	return trimmed, nil
}

// looksLikeUUID is intentionally lenient — we accept any string with
// the 8-4-4-4-12 hex shape rather than validating version bits.
// Clients in practice always send UUIDv4, but a stricter check would
// break in unexpected ways if a future client picks UUIDv7 (or
// similar) and we're not the source of the spec.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHexDigit(byte(c)) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'f') ||
		(b >= 'A' && b <= 'F')
}
