package auth

import "context"

// ctxKey is unexported so other packages can't accidentally collide with
// our context keys. The empty struct adds zero overhead.
type ctxKey struct{}

var userIDKey = ctxKey{}

// WithUserID returns a context that carries the authenticated user ID.
// Used by middleware after verifying a JWT.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// UserIDFrom retrieves the authenticated user ID from the context.
// The second return value is false when no user is attached, which is
// the expected state on routes that haven't been wrapped in RequireUser.
func UserIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey).(string)
	return id, ok && id != ""
}
