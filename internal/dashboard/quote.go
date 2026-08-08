package dashboard

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
	"github.com/Prog-Strength/prog-strength-api/internal/quotes"
)

// maxRerollOffset bounds the offset a client may ask for. quotes.PickAt
// normalizes any int, so this is not a correctness guard — it just keeps an
// absurd value out of the logs and the response, and makes the parameter's
// intent (a tap count, not a cursor into anything) explicit.
const maxRerollOffset = 10_000

// buildQuote selects the quote for this user's local day at the given reroll
// offset and narrows it to the wire shape. It takes no repository and returns
// no error: the corpus is embedded, so this cannot fail.
func buildQuote(userID, localDate string, offset int) QuoteSection {
	q := quotes.PickAt(userID, localDate, offset)
	return QuoteSection{
		ID:     q.ID,
		Text:   q.Text,
		Author: q.Author,
		Source: q.Source,
		Offset: offset,
	}
}

// quote handles GET /dashboard/quote — the reroll behind the tile's "new quote"
// button.
//
// The dashboard summary already carries the day's quote at offset 0, so this
// endpoint exists only to advance past it: the client holds the offset it is
// showing and requests offset+1. Rerolling is deliberately not persisted, so a
// reload returns to the day's quote — that costs a table and a write path to
// change, and the tile is stable across a refresh either way.
//
// Requires the same `timezone` param as the summary, for the same reason: the
// day's quote must turn over at the user's local midnight, not UTC's.
func (h *Handler) quote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID, ok := auth.UserIDFrom(ctx)
	if !ok {
		httpresp.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tz := r.URL.Query().Get("timezone")
	if tz == "" {
		httpresp.Error(w, http.StatusBadRequest, "timezone is required")
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid timezone "+tz)
		return
	}

	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "offset must be an integer")
			return
		}
		if offset < 0 || offset > maxRerollOffset {
			httpresp.Error(w, http.StatusBadRequest, "offset out of range")
			return
		}
	}

	localDate := h.now().In(loc).Format("2006-01-02")
	httpresp.OK(w, "dashboard quote", buildQuote(userID, localDate, offset))
}
