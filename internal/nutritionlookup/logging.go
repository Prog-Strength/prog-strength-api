package nutritionlookup

import (
	"context"
	"io"
	"log/slog"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
)

// NewLogger builds this package's structured logger: JSON records to w
// (stdout in prod — the awslogs driver ships it to CloudWatch, and JSON
// lets Logs Insights auto-discover fields so
// `filter request_id = "…"` works without parsing), gated at level
// (LOG_LEVEL env; see internal/config), with every record stamped with
// the request's correlation id from context.
//
// This is the first beachhead of the repo's planned log/slog migration,
// deliberately scoped to the nutrition lookup workflow (owner-approved
// 2026-06-12 — see AGENTS.md's deferred list). When a second package
// adopts slog, lift NewLogger + requestIDHandler into their own
// internal/logging package rather than copying them.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(requestIDHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
	})})
}

// requestIDHandler decorates every record logged through a *Context
// method with the request_id minted by the requestid middleware. The
// indirection means call sites just pass ctx — no per-request logger
// plumbing, and code paths that run outside a request (startup, tests
// with bare contexts) simply log without the attribute.
type requestIDHandler struct {
	slog.Handler
}

func (h requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := requestid.FromContext(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs / WithGroup must re-wrap so the request_id stamping
// survives slog's Logger.With and group derivations.
func (h requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestIDHandler{h.Handler.WithAttrs(attrs)}
}

func (h requestIDHandler) WithGroup(name string) slog.Handler {
	return requestIDHandler{h.Handler.WithGroup(name)}
}
