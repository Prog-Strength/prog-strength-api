package dashboard

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/daterange"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
	"github.com/Prog-Strength/prog-strength-api/internal/requestid"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopconn"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
)

// recoveryHistoryDTO is the wire shape of GET /recovery/history. Recovery is a
// pointer only so it serializes as the same object /dashboard/summary nests
// under sections.recovery; it is never nil, because an endpoint whose entire
// subject is that section cannot answer by omitting it.
type recoveryHistoryDTO struct {
	Recovery *RecoverySection `json:"recovery"`
}

// recoveryHistory handles GET /recovery/history?timezone=&since=&until= — the
// RecoverySection over a caller-supplied local-date window, byte-identical in
// shape to the one /dashboard/summary serves over its fixed 31 days.
//
// Why this exists beside GET /whoop/recovery rather than enriching it: that
// route is an MCP tool surface. get_whoop_recovery returns its JSON verbatim
// into the model's context, so per-day bands, z-scores and statuses would be
// pure token cost on every agent call that touches recovery, for figures no
// coaching answer reads. A page read and an agent read want different amounts
// of the same data; two doors are cheaper than one door with a mode switch.
// Do not "simplify" the two into one.
//
// Window invariant: Baseline, HRV and BaselineTrend describe the window's FINAL
// day. They mean what the dashboard means only while until is today (the
// default). An earlier until is answered honestly — that day measured against
// its own history — but it is a different question, and a client that prints
// hrv.short_avg on the end of a curve drawn from days must keep the two in step.
func (h *Handler) recoveryHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID, ok := auth.UserIDFrom(ctx)
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	q := r.URL.Query()
	// timezone is required here, unlike on GET /whoop/recovery, which accepts
	// and ignores it because it serves stored rows and does no date arithmetic.
	// This route materializes local dates, so it cannot guess one.
	tz := q.Get("timezone")
	if tz == "" {
		httpresp.Error(w, http.StatusBadRequest, "timezone is required")
		return
	}
	loc, err := daterange.LoadTimezone(tz)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	// loc's only job is deciding which date is today; the window arithmetic
	// behind it runs in UTC because these are pure calendar dates. Walking them
	// in a zone whose DST transition lands AT midnight silently repeats one date
	// and drops another — see materializeRecoveryDays.
	now := h.now()
	y, mo, d := now.In(loc).Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)

	until, err := parseWindowDay(q.Get("until"), today)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid until (expected YYYY-MM-DD)")
		return
	}
	widest := until.AddDate(0, 0, -h.pageWindowMaxDays)
	since, err := parseWindowDay(q.Get("since"), widest)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid since (expected YYYY-MM-DD)")
		return
	}
	if since.After(until) {
		httpresp.Error(w, http.StatusBadRequest, "since must be on or before until")
		return
	}
	// A span wider than the cap is clamped, never rejected: the page asks for
	// "everything" and should get as much as the server will serve.
	if since.Before(widest) {
		since = widest
	}
	sinceStr, untilStr := since.Format("2006-01-02"), until.Format("2006-01-02")

	// Same connection gate as buildRecoverySection: absent, revoked, or an
	// errored read is a 200 with the empty section, never a 500 and never an
	// omitted key. The empty payload is the builder's own degenerate-window
	// output rather than a hand-written literal, so its unknown/zero derived
	// blocks cannot drift from the ones a real window produces.
	conn, err := h.whoopConns.Get(ctx, userID)
	if err != nil || conn.Status != whoopconn.StatusConnected {
		if err != nil && !errors.Is(err, whoopconn.ErrNotFound) {
			log.Printf("dashboard: recovery history whoop connection for %s: %v", requestid.FromContext(ctx), err)
		}
		httpresp.OK(w, "read recovery history", recoveryHistoryDTO{
			Recovery: buildRecoveryView(nil, h.recovery, "", "", now, loc),
		})
		return
	}

	// One indexed ListRange over the charted window plus its lead-in, so even
	// the oldest charted morning is measured against a full trailing sample. A
	// failed read degrades to an empty window rather than a 500, like every
	// other section read. This lead-in must stay in step with the one
	// materializeRecoveryDays re-derives from since: widening that one alone
	// under-fetches here and the extra days lose their bands with no error.
	leadInStr := since.AddDate(0, 0, -h.recovery.BaselineWindowDays()).Format("2006-01-02")
	entries := defer1(ctx, r, "recovery history", func() ([]whooprecovery.Entry, error) {
		return h.whoopRecovery.ListRange(ctx, userID, leadInStr, untilStr)
	})

	httpresp.OK(w, "read recovery history", recoveryHistoryDTO{
		Recovery: buildRecoveryView(entries, h.recovery, sinceStr, untilStr, now, loc),
	})
}

// parseWindowDay resolves one optional window bound, falling back to def when
// the caller omitted it. The result is a UTC midnight so callers can do calendar
// arithmetic on it without a zone's variable-length days getting a vote.
func parseWindowDay(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	return time.Parse("2006-01-02", s)
}
