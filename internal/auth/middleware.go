package auth

import (
	"net/http"
	"strings"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
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

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
