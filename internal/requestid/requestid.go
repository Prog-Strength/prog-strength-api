// Package requestid mints a per-request correlation id, exposes it on the
// X-Request-ID response header, and stores it in request context so the
// rest of the stack (httpresp envelopes, structured logs) can reach it
// without threading new parameters through every handler.
//
// The middleware respects an inbound X-Request-ID — useful when an upstream
// (Caddy, a load balancer, a script-level tracer) has already assigned one
// and wants the API's logs to agree with theirs.
package requestid

import (
	"context"
	"net/http"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// HeaderName is the canonical header. Lowercase usage works via Go's
// http.Header normalization, but call sites should reference this
// constant rather than the literal.
const HeaderName = "X-Request-ID"

// ctxKey is unexported to prevent collisions with other packages writing
// into request context — only this package can read or write the value.
type ctxKey int

const requestIDKey ctxKey = 0

// Middleware generates (or accepts) a request id, exposes it via the
// response header, and seeds the request context. Wire it before any
// handler that wants to log or surface the id.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get(HeaderName)
		if rid == "" {
			rid = id.New()
		}
		w.Header().Set(HeaderName, rid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, rid)))
	})
}

// FromContext returns the request id stored by Middleware, or an empty
// string if the context never passed through it (e.g. background jobs).
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
