package whoopsync

import "strings"

// RequiredScopes are the scopes a connection must carry for every ingestion
// path to function. A connection missing any of these is CONNECTED but
// DEGRADED: its tokens are valid and recovery still syncs, but the paths
// needing the absent scope are skipped rather than attempted.
//
// Deliberately NOT the same list as ScopeString: `offline` is a grant
// modifier rather than a read capability, and a connection that somehow lost
// it fails loudly at refresh rather than quietly under-scoped. Anything we
// request but do not yet consume must stay OUT of this list, or the
// under-scoped gauge lights up for a capability nothing reads.
var RequiredScopes = []string{"read:recovery", "read:cycles", "read:sleep", "read:profile"}

// MissingScopes returns the RequiredScopes absent from a connection's granted
// scope string, in RequiredScopes order. granted is WHOOP's echoed-back
// space-separated scope string as persisted on the connection row, so this is
// a pure function over data we already hold — no WHOOP call, no migration.
// The returned slice is nil (not empty) when nothing is missing, so callers
// can test it with len() or against nil interchangeably.
func MissingScopes(granted string) []string {
	have := make(map[string]bool, len(RequiredScopes))
	for _, s := range strings.Fields(granted) {
		have[s] = true
	}
	var missing []string
	for _, s := range RequiredScopes {
		if !have[s] {
			missing = append(missing, s)
		}
	}
	return missing
}
