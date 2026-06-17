// Package logging is the repo's shared structured-logging home: a JSON slog
// logger whose every record is stamped with the request's correlation id from
// context.
//
// JSON to stdout is deliberate — the awslogs driver ships stdout to
// CloudWatch, and JSON lets Logs Insights auto-discover fields so
// `filter request_id = "…"` works without parsing. The level is gated by
// LOG_LEVEL (see internal/config), so info is the prod default and debug can
// be switched on for an incident without a code change.
//
// This was lifted out of internal/nutritionlookup when the planned-workout
// handler became the second slog adopter (the migration note there called for
// exactly this move). nutritionlookup.NewLogger now delegates here.
package logging

import (
	"context"
	"io"
	"log/slog"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
)

// NewLogger builds a JSON slog logger writing to w, gated at level, with every
// record stamped with the request's correlation id from context.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(requestIDHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
	})})
}

// requestIDHandler decorates every record logged through a *Context method
// with the request_id minted by the requestid middleware. The indirection
// means call sites just pass ctx — no per-request logger plumbing, and code
// paths that run outside a request (startup, tests with bare contexts) simply
// log without the attribute.
type requestIDHandler struct {
	slog.Handler
}

func (h requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := requestid.FromContext(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs / WithGroup must re-wrap so the request_id stamping survives slog's
// Logger.With and group derivations.
func (h requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestIDHandler{h.Handler.WithAttrs(attrs)}
}

func (h requestIDHandler) WithGroup(name string) slog.Handler {
	return requestIDHandler{h.Handler.WithGroup(name)}
}
