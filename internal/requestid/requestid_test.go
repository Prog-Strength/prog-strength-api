package requestid_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
)

// TestMiddleware_GeneratesIDWhenAbsent verifies that an incoming request
// without an X-Request-ID header receives a generated ID that is exposed
// both via the response header and the request context.
func TestMiddleware_GeneratesIDWhenAbsent(t *testing.T) {
	var ctxID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID = requestid.FromContext(r.Context())
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	requestid.Middleware(next).ServeHTTP(w, r)

	hdrID := w.Header().Get(requestid.HeaderName)
	if hdrID == "" {
		t.Fatalf("response header %s empty; expected a generated id", requestid.HeaderName)
	}
	if ctxID == "" {
		t.Fatalf("context id empty; expected a generated id")
	}
	if hdrID != ctxID {
		t.Fatalf("header id %q != context id %q; they must match", hdrID, ctxID)
	}
}

// TestMiddleware_RespectsIncomingHeader verifies that a client-provided
// X-Request-ID is propagated through context + echoed in the response
// header rather than being replaced with a new id. This matters for
// upstream tracing — Caddy or a load balancer can attach an id and the
// API logs will agree with the edge logs.
func TestMiddleware_RespectsIncomingHeader(t *testing.T) {
	const incoming = "trace-from-edge-abc123"

	var ctxID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxID = requestid.FromContext(r.Context())
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	r.Header.Set(requestid.HeaderName, incoming)
	requestid.Middleware(next).ServeHTTP(w, r)

	if got := w.Header().Get(requestid.HeaderName); got != incoming {
		t.Fatalf("response header = %q, want %q", got, incoming)
	}
	if ctxID != incoming {
		t.Fatalf("context id = %q, want %q", ctxID, incoming)
	}
}

// TestFromContext_EmptyWhenAbsent guards the helper against panics or
// non-empty defaults when called on a context that never passed through
// the middleware (e.g. background jobs).
func TestFromContext_EmptyWhenAbsent(t *testing.T) {
	if got := requestid.FromContext(context.Background()); got != "" {
		t.Fatalf("FromContext on background ctx = %q, want empty string", got)
	}
}
