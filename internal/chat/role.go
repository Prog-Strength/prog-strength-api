package chat

// Role mirrors the Anthropic Messages API role enum we use on the
// agent side. Closed set on purpose: any new value (system,
// tool_use, etc.) needs a code change here AND in the schema's
// CHECK constraint to stay in sync.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Valid reports whether r is one of the two recognized values.
// Handler-layer validation rejects unknown roles with 400 before
// the repo sees them; the repo also calls Valid so the in-memory
// tests catch regressions without an HTTP round-trip.
func (r Role) Valid() bool {
	return r == RoleUser || r == RoleAssistant
}
