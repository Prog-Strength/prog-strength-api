package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/quotes"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

// rerollQuote drives POST /dashboard/quote/reroll for userID with the given
// query string.
func rerollQuote(t *testing.T, r *chi.Mux, userID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/dashboard/quote/reroll"+query, nil)
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

// mustCreateUser seeds a second user, so per-user isolation can be asserted
// against a real row rather than an unseeded id.
func mustCreateUser(t *testing.T, rp *repos, email string) string {
	t.Helper()
	u := &user.User{
		Email:        email,
		DisplayName:  email,
		WeightUnit:   user.WeightUnitPounds,
		DistanceUnit: user.DistanceUnitMiles,
		Timezone:     "UTC",
	}
	if err := rp.user.Create(context.Background(), u); err != nil {
		t.Fatalf("create user %q: %v", email, err)
	}
	return u.ID
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

func TestQuote_CarriesTheCorpusLinks(t *testing.T) {
	// buildQuote narrows quotes.Quote to the wire shape by hand, so a field
	// added to the corpus and forgotten here is silently dropped on the way
	// out. Walking the whole corpus by reroll offset checks the mapping
	// against every quote rather than whichever one today happens to serve.
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	want := map[string]quotes.Quote{}
	for _, q := range quotes.All() {
		want[q.ID] = q
	}

	linked := 0
	for i := range quotes.All() {
		got := decodeQuote(t, rerollQuote(t, r, userID, "?timezone=UTC"))
		src, ok := want[got.ID]
		if !ok {
			t.Fatalf("reroll %d served unknown quote %q", i, got.ID)
		}
		if got.AuthorURL != src.AuthorURL {
			t.Errorf("%s: author_url = %q, want %q", got.ID, got.AuthorURL, src.AuthorURL)
		}
		if got.SourceURL != src.SourceURL {
			t.Errorf("%s: source_url = %q, want %q", got.ID, got.SourceURL, src.SourceURL)
		}
		if got.AuthorURL != "" {
			linked++
		}
	}
	// Guards the assertions above against passing vacuously if the corpus
	// ever loses every link.
	if linked == 0 {
		t.Error("no quote in the corpus carries an author_url; the mapping above proved nothing")
	}
}

func TestQuote_RerollReturnsADifferentQuote(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	day := decode(t, get(t, r, userID, "?timezone=UTC")).Quote
	next := decodeQuote(t, rerollQuote(t, r, userID, "?timezone=UTC"))
	if next.ID == day.ID {
		t.Errorf("reroll returned the same quote %q; the button would look broken", day.ID)
	}
	if next.Offset != 1 {
		t.Errorf("echoed offset = %d, want 1", next.Offset)
	}

	// Walking the corpus must not repeat before it wraps.
	seen := map[string]bool{day.ID: true, next.ID: true}
	for i := 2; i < len(quotes.All()); i++ {
		q := decodeQuote(t, rerollQuote(t, r, userID, "?timezone=UTC"))
		if seen[q.ID] {
			t.Fatalf("reroll %d repeated quote %q before wrapping", i, q.ID)
		}
		seen[q.ID] = true
	}
}

// The behavior this table exists for: the reroll has to still be there on the
// next page load. Before it, the summary always served offset 0 and the tile
// snapped back to the day's quote on refresh.
func TestQuote_RerollSurvivesAReload(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	rerolled := decodeQuote(t, rerollQuote(t, r, userID, "?timezone=UTC"))

	for i := 0; i < 3; i++ {
		reloaded := decode(t, get(t, r, userID, "?timezone=UTC")).Quote
		if reloaded.ID != rerolled.ID {
			t.Fatalf("reload %d served %q, want the rerolled %q", i, reloaded.ID, rerolled.ID)
		}
		if reloaded.Offset != rerolled.Offset {
			t.Errorf("reload %d offset = %d, want %d", i, reloaded.Offset, rerolled.Offset)
		}
	}
}

// The server owns the offset, so successive taps advance from the STORED
// position. A client that sent its own offset could not produce this: each of
// these requests is byte-identical, and they must still walk forward.
func TestQuote_SuccessiveRerollsAdvanceWithoutClientState(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	for want := 1; want <= 3; want++ {
		got := decodeQuote(t, rerollQuote(t, r, userID, "?timezone=UTC"))
		if got.Offset != want {
			t.Errorf("reroll %d echoed offset %d, want %d", want, got.Offset, want)
		}
	}
}

// Rerolling a full lap must not store an ever-growing counter.
func TestQuote_StoredOffsetWrapsWithinOneLap(t *testing.T) {
	ctx := context.Background()
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	n := len(quotes.All())
	for i := 0; i < n+2; i++ {
		rerollQuote(t, r, userID, "?timezone=UTC")
	}

	stored, err := rp.quoteReroll.Get(ctx, userID)
	if err != nil {
		t.Fatalf("get stored reroll: %v", err)
	}
	if stored.Offset < 0 || stored.Offset >= n {
		t.Errorf("stored offset = %d after %d rerolls, want it folded into [0,%d)", stored.Offset, n+2, n)
	}
}

func TestQuote_RerollExpiresWithTheLocalDay(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	// testNow is 2026-06-17 13:00 UTC, which is already 2026-06-18 in UTC+14.
	// Rerolling as a UTC client stores the reroll against 2026-06-17, so the
	// UTC+14 client — already on the next local day — must see that day's
	// quote rather than inheriting a reroll that has lapsed for them.
	rerollQuote(t, r, userID, "?timezone=UTC")

	// Asserted against Pick for the new day rather than "differs from the
	// rerolled quote": the two are equal for one offset in every corpus-sized
	// lap, so an inequality check would be a coin flip that a future quote
	// could turn into a spurious failure.
	nextDay := decode(t, get(t, r, userID, "?timezone=Pacific/Kiritimati")).Quote
	if want := quotes.Pick(userID, "2026-06-18"); nextDay.ID != want.ID {
		t.Errorf("quote for UTC+14 = %q, want the 2026-06-18 quote %q", nextDay.ID, want.ID)
	}
	if nextDay.Offset != 0 {
		t.Errorf("offset = %d after the local day rolled over, want 0", nextDay.Offset)
	}
}

func TestQuote_LocalDateDecidesTheDay(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	// testNow is 2026-06-17 13:00 UTC, which is already 2026-06-18 in UTC+14 —
	// so that client must be served the NEXT day's quote, not UTC's.
	got := decode(t, get(t, r, userID, "?timezone=Pacific/Kiritimati")).Quote
	want := quotes.Pick(userID, "2026-06-18")
	if got.ID != want.ID {
		t.Errorf("quote for UTC+14 = %q, want the 2026-06-18 quote %q", got.ID, want.ID)
	}
}

// The reroll is per user: one user's tap must not move anyone else's tile.
func TestQuote_RerollIsPerUser(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	enableQuoteTile(t, rp, userID)

	other := mustCreateUser(t, rp, "quote-other@example.com")
	enableQuoteTile(t, rp, other)

	otherBefore := decode(t, get(t, r, other, "?timezone=UTC")).Quote
	rerollQuote(t, r, userID, "?timezone=UTC")
	otherAfter := decode(t, get(t, r, other, "?timezone=UTC")).Quote

	if otherAfter.ID != otherBefore.ID {
		t.Errorf("another user's reroll moved this user's quote from %q to %q", otherBefore.ID, otherAfter.ID)
	}
	if otherAfter.Offset != 0 {
		t.Errorf("offset = %d, want 0 for a user who never rerolled", otherAfter.Offset)
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := rerollQuote(t, r, userID, tc.query); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
