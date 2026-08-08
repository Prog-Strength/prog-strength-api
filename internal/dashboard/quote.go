package dashboard

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
	"github.com/Prog-Strength/prog-strength-api/internal/quotes"
	"github.com/Prog-Strength/prog-strength-api/internal/requestid"
)

// buildQuote selects the quote for this user's local day at the given reroll
// offset and narrows it to the wire shape. It takes no repository and returns
// no error: the corpus is embedded, so this cannot fail.
func buildQuote(userID, localDate string, offset int) QuoteSection {
	q := quotes.PickAt(userID, localDate, offset)
	return QuoteSection{
		ID:        q.ID,
		Text:      q.Text,
		Author:    q.Author,
		AuthorURL: q.AuthorURL,
		Source:    q.Source,
		SourceURL: q.SourceURL,
		Offset:    offset,
	}
}

// resolveQuoteOffset returns the offset this user's quote should be served at
// for localDate: their stored reroll when they made one today, and 0 otherwise.
//
// "Otherwise" covers three cases that all mean the same thing to a reader —
// never rerolled, rerolled on an earlier day, or the row could not be read —
// and all resolve to the day's quote. The last is the deliberate one: this is
// a decorative tile, and the handler's rule is that a recoverable repo error
// degrades a section rather than failing the request. The user sees the
// unrerolled quote, which is exactly what they saw before this table existed.
func (h *Handler) resolveQuoteOffset(ctx context.Context, r *http.Request, userID, localDate string) int {
	stored, err := h.quoteRerollRepo.Get(ctx, userID)
	switch {
	case errors.Is(err, ErrQuoteRerollNotFound):
		return 0
	case err != nil:
		log.Printf("dashboard: %s for %s: %v", "read quote reroll", requestid.FromContext(r.Context()), err)
		return 0
	}
	// A row from an earlier local day has lapsed: the daily quote turns over at
	// the user's local midnight, and so does the reroll that walked away from it.
	if stored.LocalDate != localDate {
		return 0
	}
	return stored.Offset
}

// rerollQuote handles POST /dashboard/quote/reroll — the tile's "new quote"
// button.
//
// The SERVER owns the offset. The client sends no position and tracks none: it
// asks to advance, and the stored offset moves by one. Letting the client send
// offset+1 instead would make two open tabs each advance from their own stale
// idea of where the user is, and the last writer would win with a number that
// was never on screen.
//
// A POST rather than a GET because it writes. The advance is deliberately not
// idempotent — that is what a reroll *is* — so it must not sit behind a verb
// that promises otherwise.
//
// Requires the same `timezone` param as the summary, for the same reason: the
// stored date has to be the user's local one, or the reroll would expire at
// UTC midnight instead of theirs.
func (h *Handler) rerollQuote(w http.ResponseWriter, r *http.Request) {
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

	localDate := h.now().In(loc).Format("2006-01-02")
	next := h.resolveQuoteOffset(ctx, r, userID, localDate) + 1

	// quotes.PickAt normalizes any int, so an unbounded offset is not a
	// correctness problem — but it is stored, and a counter that only ever
	// grows would eventually write an absurd number into the row. Folding it
	// back into one lap of the corpus keeps the stored value meaningful and
	// costs nothing: offset n and offset n%len are the same quote.
	if n := len(quotes.All()); n > 0 {
		next %= n
	}

	// A failed write costs the user persistence, not the reroll. Serving the
	// advanced quote anyway keeps the button working on a degraded database —
	// it just won't survive the refresh, which is the behavior this endpoint
	// replaced.
	if err := h.quoteRerollRepo.Upsert(ctx, userID, localDate, next); err != nil {
		log.Printf("dashboard: %s for %s: %v", "write quote reroll", requestid.FromContext(r.Context()), err)
	}

	httpresp.OK(w, "dashboard quote", buildQuote(userID, localDate, next))
}
