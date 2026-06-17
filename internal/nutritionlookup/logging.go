package nutritionlookup

import (
	"io"
	"log/slog"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/logging"
)

// NewLogger builds the lookup workflow's structured logger. The request-id
// stamping + JSON-to-CloudWatch wiring now lives in internal/logging (lifted
// there once the planned-workout handler became the second slog adopter); this
// stays as a thin alias so the package's existing call sites and tests keep
// referring to nutritionlookup.NewLogger.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return logging.NewLogger(w, level)
}
