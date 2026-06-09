package usage

import (
	"errors"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler serves GET /me/usage: the authenticated user's daily-spend
// percentage, capped flag, and reset time. It depends on a *Ledger for
// the spend computation and the env-configured dollar cap.
type Handler struct {
	ledger *Ledger
	capUSD float64
	now    func() time.Time // injectable for tests

	// uncappedWarnOnce guards a single log line when cap <= 0, so a
	// misconfigured (or deliberately disabled) cap doesn't spam logs on
	// every request.
	uncappedWarnOnce sync.Once
}

func NewHandler(ledger *Ledger, capUSD float64) *Handler {
	return &Handler{ledger: ledger, capUSD: capUSD, now: time.Now}
}

// Mount registers the route on the JWT-gated group (the user ID is read
// from request context, which auth.RequireUser populates).
func (h *Handler) Mount(r chi.Router) {
	r.Get("/me/usage", h.getMyUsage)
}

// usageResponse is the GET /me/usage payload. It is a typed struct with
// NO usd field by construction — no rename or tag mistake can leak the
// operator's dollar figure to the client.
type usageResponse struct {
	PercentUsed int    `json:"percent_used"`
	Capped      bool   `json:"capped"`
	ResetsAt    string `json:"resets_at"`
}

func (h *Handler) getMyUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	// tz is optional; LocalDayWindow falls back to UTC on empty/invalid.
	tz := r.URL.Query().Get("tz")
	startUTC, endUTC := LocalDayWindow(h.now(), tz)

	spend, err := h.ledger.SpendTodayUSD(r.Context(), userID, startUTC, endUTC)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "compute daily usage", err)
		return
	}

	resp := usageResponse{
		ResetsAt: endUTC.Format(time.RFC3339),
	}

	// cap <= 0 means usage capping is disabled (or misconfigured). Treat
	// as uncapped: 0% and never capped, so the bar renders empty and the
	// gate never blocks. Log once to surface the misconfiguration.
	if h.capUSD <= 0 {
		h.uncappedWarnOnce.Do(func() {
			log.Printf("usage: DAILY_USAGE_CAP_USD <= 0; usage reported as uncapped")
		})
	} else {
		resp.PercentUsed = int(math.Min(100, math.Round(spend/h.capUSD*100)))
		resp.Capped = spend >= h.capUSD
	}

	if resp.Capped {
		cappedTotal.Inc()
	}

	httpresp.OK(w, "got usage", resp)
}
