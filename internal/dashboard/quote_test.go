package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/quotes"
)

// getQuote drives GET /dashboard/quote for userID with the given query string.
func getQuote(t *testing.T, r *chi.Mux, userID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/quote"+query, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeQuote(t *testing.T, rec *httptest.ResponseRecorder) QuoteSection {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Message string       `json:"message"`
		Data    QuoteSection `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return env.Data
}

// enableQuoteTile stores a layout containing the quote tile, since the default
// layout deliberately omits it (the tile is opt-in from the add-tile tray).
func enableQuoteTile(t *testing.T, rp *repos, userID string) {
	t.Helper()
	if err := rp.layout.Upsert(context.Background(), userID, []TileID{TileQuote}); err != nil {
		t.Fatalf("upsert layout: %v", err)
	}
}

func TestSummary_QuoteAbsentFromDefaultLayout(t *testing.T) {
	r, _, userID := newTestEnv(t)

	var body map[string]any
	rec := get(t, r, userID, "?timezone=UTC")
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := body["data"].(map[string]any)
	if _, present := data["quote"]; present {
		t.Error("quote key present in the default-layout summary; the tile is opt-in")
	}
}

func TestSummary_QuotePresentWhenTileEnabled(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	got := decode(t, get(t, r, userID, "?timezone=UTC")).Quote
	if got.Text == "" || got.Author == "" {
		t.Fatalf("quote section is not populated: %+v", got)
	}
	if got.Offset != 0 {
		t.Errorf("summary quote offset = %d, want 0 (the day's quote)", got.Offset)
	}
	// The summary must agree with the service for this user's local day.
	want := quotes.Pick(userID, "2026-06-17")
	if got.ID != want.ID {
		t.Errorf("summary quote = %q, want %q", got.ID, want.ID)
	}
}

func TestSummary_QuoteIsStableAcrossRequests(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	first := decode(t, get(t, r, userID, "?timezone=UTC")).Quote.ID
	for i := 0; i < 5; i++ {
		if got := decode(t, get(t, r, userID, "?timezone=UTC")).Quote.ID; got != first {
			t.Fatalf("refresh %d changed the quote: %q, want %q", i, got, first)
		}
	}
}

func TestQuote_DefaultOffsetMatchesSummary(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	fromSummary := decode(t, get(t, r, userID, "?timezone=UTC")).Quote
	fromEndpoint := decodeQuote(t, getQuote(t, r, userID, "?timezone=UTC"))
	if fromEndpoint.ID != fromSummary.ID {
		t.Errorf("endpoint quote = %q, want the summary's %q", fromEndpoint.ID, fromSummary.ID)
	}
	if fromEndpoint.Offset != 0 {
		t.Errorf("offset = %d, want 0", fromEndpoint.Offset)
	}
}

func TestQuote_OffsetRerollsToADifferentQuote(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	day := decodeQuote(t, getQuote(t, r, userID, "?timezone=UTC"))
	next := decodeQuote(t, getQuote(t, r, userID, "?timezone=UTC&offset=1"))
	if next.ID == day.ID {
		t.Errorf("offset=1 returned the same quote %q; the reroll button would look broken", day.ID)
	}
	if next.Offset != 1 {
		t.Errorf("echoed offset = %d, want 1", next.Offset)
	}

	// Walking the corpus must not repeat before it wraps.
	seen := map[string]bool{day.ID: true}
	for i := 1; i < len(quotes.All()); i++ {
		q := decodeQuote(t, getQuote(t, r, userID, "?timezone=UTC&offset="+strconv.Itoa(i)))
		if seen[q.ID] {
			t.Fatalf("offset=%d repeated quote %q before wrapping", i, q.ID)
		}
		seen[q.ID] = true
	}
}

func TestQuote_LocalDateDecidesTheDay(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	// testNow is 2026-06-17 13:00 UTC, which is already 2026-06-18 in UTC+14 —
	// so that client must be served the NEXT day's quote, not UTC's.
	got := decodeQuote(t, getQuote(t, r, userID, "?timezone=Pacific/Kiritimati"))
	want := quotes.Pick(userID, "2026-06-18")
	if got.ID != want.ID {
		t.Errorf("quote for UTC+14 = %q, want the 2026-06-18 quote %q", got.ID, want.ID)
	}
}

func TestQuote_RejectsBadRequests(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	cases := []struct {
		name  string
		query string
	}{
		{"missing timezone", ""},
		{"invalid timezone", "?timezone=Mars/Olympus"},
		{"non-numeric offset", "?timezone=UTC&offset=soon"},
		{"negative offset", "?timezone=UTC&offset=-1"},
		{"absurd offset", "?timezone=UTC&offset=99999999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := getQuote(t, r, userID, tc.query); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
