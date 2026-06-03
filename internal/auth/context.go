package auth

import (
	"context"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// The context-key plumbing lives in the authctx leaf package so that
// packages auth depends on (e.g. user) can read the user ID without an
// import cycle. These thin wrappers keep the auth.WithUserID / auth.UserIDFrom
// surface that existing handlers already use.

// WithUserID returns a context that carries the authenticated user ID.
// Used by middleware after verifying a JWT.
func WithUserID(ctx context.Context, userID string) context.Context {
	return authctx.WithUserID(ctx, userID)
}

// UserIDFrom retrieves the authenticated user ID from the context.
// The second return value is false when no user is attached, which is
// the expected state on routes that haven't been wrapped in RequireUser.
func UserIDFrom(ctx context.Context) (string, bool) {
	return authctx.UserIDFrom(ctx)
}
