package nutritionlookup

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler exposes GET /nutrition/lookup. The endpoint is auth-gated not
// because food data is private but because it spends shared provider
// quota — gating keeps anonymous internet traffic off the FatSecret
// budget.
type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Mount registers routes under the given router. Callers are expected
// to have already wrapped the router in auth.RequireUser middleware —
// the handler reads the user ID from request context and assumes it's
// present (identity isn't otherwise used; lookups are global).
func (h *Handler) Mount(r chi.Router) {
	r.Get("/nutrition/lookup", h.lookup)
}

func (h *Handler) lookup(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserIDFrom(r.Context()); !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	q := r.URL.Query()

	query := strings.TrimSpace(q.Get("query"))
	if query == "" {
		httpresp.Error(w, http.StatusBadRequest, "query is required")
		return
	}
	if len(query) > 200 {
		httpresp.Error(w, http.StatusBadRequest, "query is too long (max 200 characters)")
		return
	}

	quantity := 1.0
	if raw := q.Get("quantity"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			httpresp.Error(w, http.StatusBadRequest, "quantity must be a positive number")
			return
		}
		quantity = v
	}

	maxResults := 5
	if raw := q.Get("max_results"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 10 {
			httpresp.Error(w, http.StatusBadRequest, "max_results must be an integer between 1 and 10")
			return
		}
		maxResults = v
	}

	started := time.Now()
	result, err := h.svc.Lookup(r.Context(), query, quantity, maxResults)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnavailable):
			// 503 is honest REST for "dependency down/absent"; the MCP
			// forwarder adapts it into the structured error shape the
			// agent prompt handles.
			httpresp.Error(w, http.StatusServiceUnavailable, "lookup_unavailable: no nutrition data providers configured")
		case errors.Is(err, ErrFailed):
			detail := strings.TrimPrefix(err.Error(), ErrFailed.Error()+": ")
			httpresp.Error(w, http.StatusServiceUnavailable, "lookup_failed: "+detail)
		default:
			httpresp.ServerError(w, r.Context(), "nutrition lookup", err)
		}
		h.log.WarnContext(r.Context(), "lookup request failed",
			"query", query, "quantity", quantity,
			"elapsed_ms", time.Since(started).Milliseconds(), "error", err)
		return
	}
	// The request-level summary line: one INFO record per served
	// lookup, carrying request_id (via the handler wrapper) so a
	// CloudWatch `filter request_id = "…"` finds the entry point and
	// the service/provider records around it tell the full story.
	h.log.InfoContext(r.Context(), "lookup request served",
		"query", query, "quantity", quantity, "max_results", maxResults,
		"matches", len(result.Matches),
		"elapsed_ms", time.Since(started).Milliseconds())
	httpresp.OK(w, "nutrition lookup results", result)
}
