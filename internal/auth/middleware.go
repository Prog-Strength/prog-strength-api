package auth

import (
	"net/http"
	"strings"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// RequireUser returns middleware that validates the bearer token on the
// Authorization header and injects the user ID into the request context.
// Requests without a valid token are rejected with 401.
//
// Handlers behind this middleware should read the user ID with
// auth.UserIDFrom(r.Context()).
func RequireUser(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := bearerToken(r)
			if tokenStr == "" {
				httpresp.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			userID, err := Parse(tokenStr, secret)
			if err != nil {
				httpresp.Error(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), userID)))
		})
	}
}

// RequireAdmin returns middleware that authorizes the request only when the
// authenticated user's email is in adminEmails. It must be mounted INSIDE a
// RequireUser group: it reads the user ID that RequireUser injected into the
// context, resolves it to an email via the user repository, and compares
// (case-insensitively) against the admin set. Every failure mode — no user
// in context, an empty adminEmails list (fail-closed: the admin surface is
// inert until an operator is configured), an unresolvable user, or a
// non-member email — is a 403. The normalized admin set is built once here,
// not per request.
func RequireAdmin(users user.Repository, adminEmails []string) func(http.Handler) http.Handler {
	admins := make(map[string]struct{}, len(adminEmails))
	for _, e := range adminEmails {
		if normalized := strings.ToLower(strings.TrimSpace(e)); normalized != "" {
			admins[normalized] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Empty allowlist denies everyone (fail-closed).
			if len(admins) == 0 {
				httpresp.Error(w, http.StatusForbidden, "admin access required")
				return
			}
			userID, ok := authctx.UserIDFrom(r.Context())
			if !ok {
				httpresp.Error(w, http.StatusForbidden, "admin access required")
				return
			}
			u, err := users.GetByID(r.Context(), userID)
			if err != nil {
				httpresp.Error(w, http.StatusForbidden, "admin access required")
				return
			}
			if _, ok := admins[strings.ToLower(strings.TrimSpace(u.Email))]; !ok {
				httpresp.Error(w, http.StatusForbidden, "admin access required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
