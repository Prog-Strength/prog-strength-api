// Package originmatch matches a URL origin (scheme + host) against a
// whitelist of patterns, where each pattern may contain a single "*"
// wildcard.
//
// It backs the OAuth return_to open-redirect guard and intentionally
// shares the wildcard semantics of the CORS origin check (go-chi/cors):
// Vercel preview deployments carry one dynamic segment per branch/commit
// in their hostname, e.g.
//
//	https://prog-strength-web-git-dx-calendar-view-<scope>.vercel.app
//
// so a single project-scoped pattern like
//
//	https://prog-strength-web-*-<scope>.vercel.app
//
// matches every branch preview without per-branch ops. Scope the wildcard
// to your project + Vercel scope — never a bare "*.vercel.app", which would
// reopen the open-redirect hole the whitelist exists to close.
package originmatch

import (
	"net/url"
	"strings"
)

// AllowReturnTo reports whether returnTo's origin (scheme + host) matches any
// pattern in allowed. Patterns without "*" match exactly, which keeps strict
// behavior for custom schemes like "progstrength://" whose Host is empty (so
// "progstrength://evil.example.com" never matches the literal "progstrength://"
// entry). Patterns with a single "*" match by prefix + suffix.
func AllowReturnTo(returnTo string, allowed []string) bool {
	u, err := url.Parse(returnTo)
	if err != nil || u.Scheme == "" {
		return false
	}
	origin := u.Scheme + "://" + u.Host
	return MatchOrigin(origin, allowed)
}

// MatchOrigin reports whether origin matches any of patterns, each of which may
// contain a single "*" wildcard. Matching mirrors go-chi/cors: an exact string
// compare when the pattern has no "*", otherwise a prefix/suffix check around
// the wildcard with a length guard so the prefix and suffix cannot overlap.
func MatchOrigin(origin string, patterns []string) bool {
	for _, p := range patterns {
		if matchOne(origin, p) {
			return true
		}
	}
	return false
}

func matchOne(origin, pattern string) bool {
	i := strings.IndexByte(pattern, '*')
	if i < 0 {
		return origin == pattern
	}
	prefix := pattern[:i]
	suffix := pattern[i+1:]
	return len(origin) >= len(prefix)+len(suffix) &&
		strings.HasPrefix(origin, prefix) &&
		strings.HasSuffix(origin, suffix)
}
