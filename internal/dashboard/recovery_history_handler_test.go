package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/recoverytrend"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopconn"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
)

// --- harness ----------------------------------------------------------

// getHistory drives GET /recovery/history for userID. An empty userID sends the
// request with no authed user in context, which is the 401 path.
func getHistory(t *testing.T, r *chi.Mux, userID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/recovery/history"+query, nil)
	if userID != "" {
		req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// historyRecovery asserts 200 and returns the response's `recovery` object both
// raw (so a test can prove days is [] rather than null or absent) and decoded.
func historyRecovery(t *testing.T, rec *httptest.ResponseRecorder) (json.RawMessage, RecoverySection) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	raw, ok := env.Data["recovery"]
	if !ok {
		t.Fatalf("recovery key absent from data; body=%s", rec.Body.String())
	}
	var section RecoverySection
	if err := json.Unmarshal(raw, &section); err != nil {
		t.Fatalf("decode recovery section: %v; raw=%s", err, raw)
	}
	return raw, section
}

// historyRouter mounts a Handler carrying only the collaborators this read
// touches — the connection gate, the recovery repo, the engine, the clock and
// the window cap — so window assertions don't need the whole dashboard wired.
func historyRouter(conns whoopconn.Repository, rec whooprecovery.Repository, now time.Time, maxDays int) *chi.Mux {
	h := &Handler{
		whoopConns:        conns,
		whoopRecovery:     rec,
		recovery:          testRecoveryEngine(),
		pageWindowMaxDays: maxDays,
		now:               func() time.Time { return now },
	}
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

// errRecoveryRepo fails every ListRange, proving a recovery read error degrades
// to an empty window rather than failing the request.
type errRecoveryRepo struct {
	whooprecovery.Repository
}

func (errRecoveryRepo) ListRange(context.Context, string, string, string) ([]whooprecovery.Entry, error) {
	return nil, errors.New("recovery list boom")
}

// seedRecoveryEntries writes the fixture entries through the real repository so
// the route reads them back over its own indexed range query.
func seedRecoveryEntries(t *testing.T, rp *repos, userID string, entries []whooprecovery.Entry) {
	t.Helper()
	for _, e := range entries {
		e.UserID = userID
		if err := rp.whoopRec.Upsert(context.Background(), e, testNow); err != nil {
			t.Fatalf("seed recovery %s: %v", e.Date, err)
		}
	}
}

// --- validation -------------------------------------------------------

func TestRecoveryHistory_Unauthenticated(t *testing.T) {
	r, _, _ := newTestEnv(t)
	rec := getHistory(t, r, "", "?timezone=UTC")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestRecoveryHistory_BadRequests pins the boundary, first-error-wins. timezone
// is required here (unlike on GET /whoop/recovery) because this route
// materializes local dates rather than echoing stored ones.
func TestRecoveryHistory_BadRequests(t *testing.T) {
	r, _, userID := newTestEnv(t)

	tests := []struct {
		name    string
		query   string
		wantMsg string
	}{
		{"missing timezone", "", "timezone is required"},
		{"unloadable timezone", "?timezone=Not/AZone", "invalid timezone Not/AZone"},
		{"malformed since", "?timezone=UTC&since=last-tuesday", "invalid since"},
		{"malformed until", "?timezone=UTC&until=2026-13-45", "invalid until"},
		// until is parsed first, so it wins even though since is malformed too.
		{"both malformed reports until", "?timezone=UTC&since=nope&until=nope", "invalid until"},
		{"since after until", "?timezone=UTC&since=2026-06-10&until=2026-06-01", "since must be on or before until"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := getHistory(t, r, userID, tt.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantMsg) {
				t.Errorf("body = %s, want it to contain %q", rec.Body.String(), tt.wantMsg)
			}
		})
	}
}

// --- the empty states -------------------------------------------------

// TestRecoveryHistory_NotConnected: an endpoint whose whole subject is the
// recovery section cannot answer by omitting it, so a user with no Whoop
// connection gets 200 and the well-formed empty shape — days [] rather than a
// window of nulls, today null, and the three derived blocks unknown.
func TestRecoveryHistory_NotConnected(t *testing.T) {
	r, _, userID := newTestEnv(t)

	raw, section := historyRecovery(t, getHistory(t, r, userID, "?timezone=UTC"))

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode recovery object: %v; raw=%s", err, raw)
	}
	if string(keys["days"]) != "[]" {
		t.Errorf("days = %s, want [] (never null, never absent)", keys["days"])
	}
	if string(keys["today"]) != "null" {
		t.Errorf("today = %s, want null", keys["today"])
	}
	if section.HRV.Status != recoverytrend.StatusUnknown || section.HRV.Trend != recoverytrend.TrendUnknown {
		t.Errorf("hrv status/trend = %q/%q, want unknown/unknown", section.HRV.Status, section.HRV.Trend)
	}
	if section.Baseline.HRVAvg != nil || section.Baseline.HRVDays != 0 {
		t.Errorf("baseline hrv_avg/hrv_days = %v/%d, want null/0", section.Baseline.HRVAvg, section.Baseline.HRVDays)
	}
	if section.BaselineTrend.Direction != recoverytrend.TrendUnknown {
		t.Errorf("baseline_trend.direction = %q, want unknown", section.BaselineTrend.Direction)
	}
}

// TestRecoveryHistory_RepositoryError_DegradesToEmptyWindow: a failed recovery
// read is a 200 with the window materialized and nothing in it, matching every
// other section read — never a 500.
func TestRecoveryHistory_RepositoryError_DegradesToEmptyWindow(t *testing.T) {
	r := historyRouter(connectedWhoopConn{}, errRecoveryRepo{}, testNow, defaultPageWindowMaxDays)

	_, section := historyRecovery(t, getHistory(t, r, "user-1", "?timezone=UTC&since=2026-06-08&until=2026-06-17"))

	if len(section.Days) != 10 {
		t.Fatalf("len(days) = %d, want 10 (the window is still materialized)", len(section.Days))
	}
	for _, d := range section.Days {
		if d.HRVRmssdMilli != nil || d.RestingHeartRate != nil || d.RecoveryScore != nil {
			t.Fatalf("day %s carries metrics after a failed read: %+v", d.Date, d)
		}
	}
	if section.HRV.Status != recoverytrend.StatusUnknown {
		t.Errorf("hrv.status = %q, want unknown", section.HRV.Status)
	}
}

// --- the window -------------------------------------------------------

// TestRecoveryHistory_Window pins what the route asks the repository for and
// what it charts: the fetched range leads the charted one by
// baseline_window_days so every charted day has its lead-in, until defaults to
// today in the caller's zone, and a span wider than page_window_max_days is
// CLAMPED forward rather than rejected. The call count is asserted too — one
// indexed read per request, never a second bolted on.
func TestRecoveryHistory_Window(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	// 20:00 in Denver is already 2026-08-02 in UTC, and the clock hands the
	// handler its instant in the server's zone (like time.Now does), so the
	// default until can only come out as 2026-08-01 if it dated now in loc.
	now := time.Date(2026, 8, 1, 20, 0, 0, 0, denver).UTC()

	tests := []struct {
		name                        string
		maxDays                     int
		query                       string
		wantFetchSince, wantFetchTo string
		wantFirst, wantLast         string
		wantDays                    int
	}{
		{
			name:           "explicit window fetches its own lead-in",
			maxDays:        defaultPageWindowMaxDays,
			query:          "?timezone=America/Denver&since=2026-07-20&until=2026-08-01",
			wantFetchSince: "2026-06-20", wantFetchTo: "2026-08-01",
			wantFirst: "2026-07-20", wantLast: "2026-08-01", wantDays: 13,
		},
		{
			name:           "until defaults to today in the caller's zone, not UTC's",
			maxDays:        defaultPageWindowMaxDays,
			query:          "?timezone=America/Denver&since=2026-07-20",
			wantFetchSince: "2026-06-20", wantFetchTo: "2026-08-01",
			wantFirst: "2026-07-20", wantLast: "2026-08-01", wantDays: 13,
		},
		{
			name:           "since defaults to the cap",
			maxDays:        5,
			query:          "?timezone=America/Denver",
			wantFetchSince: "2026-06-27", wantFetchTo: "2026-08-01",
			wantFirst: "2026-07-27", wantLast: "2026-08-01", wantDays: 6,
		},
		{
			name:           "a wider request is clamped, not rejected",
			maxDays:        5,
			query:          "?timezone=America/Denver&since=2026-01-01&until=2026-08-01",
			wantFetchSince: "2026-06-27", wantFetchTo: "2026-08-01",
			wantFirst: "2026-07-27", wantLast: "2026-08-01", wantDays: 6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := &captureRecoveryRepo{}
			r := historyRouter(connectedWhoopConn{}, capture, now, tt.maxDays)

			_, section := historyRecovery(t, getHistory(t, r, "user-1", tt.query))

			if capture.since != tt.wantFetchSince || capture.until != tt.wantFetchTo {
				t.Errorf("fetched [%s, %s], want [%s, %s]", capture.since, capture.until, tt.wantFetchSince, tt.wantFetchTo)
			}
			if capture.calls != 1 {
				t.Errorf("ListRange calls = %d, want exactly 1 indexed read per request", capture.calls)
			}
			if len(section.Days) != tt.wantDays {
				t.Fatalf("len(days) = %d, want %d", len(section.Days), tt.wantDays)
			}
			if section.Days[0].Date != tt.wantFirst {
				t.Errorf("first charted date = %q, want %q", section.Days[0].Date, tt.wantFirst)
			}
			if section.Days[len(section.Days)-1].Date != tt.wantLast {
				t.Errorf("last charted date = %q, want %q", section.Days[len(section.Days)-1].Date, tt.wantLast)
			}
		})
	}
}

// --- the populated read -----------------------------------------------

// TestRecoveryHistory_ConnectedWithRows drives the whole read through the real
// repositories: the charted window is exactly [since, until] inclusive and
// oldest→newest, the FIRST charted day already carries a band (the lead-in
// proof), and all three derived blocks are populated.
func TestRecoveryHistory_ConnectedWithRows(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	seedWhoopConnected(t, rp, userID)
	seedRecoveryEntries(t, rp, userID, entriesBetween(testNow, time.UTC, 60, 0, rampHRV))

	// 39 charted dates: wider than the dashboard's window, and past
	// baseline_drift_days + 1 so baseline_trend has a past to compare against.
	_, section := historyRecovery(t, getHistory(t, r, userID, "?timezone=UTC&since=2026-05-10&until=2026-06-17"))

	if len(section.Days) != 39 {
		t.Fatalf("len(days) = %d, want 39", len(section.Days))
	}
	for i, d := range section.Days {
		want := time.Date(2026, 5, 10+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		if d.Date != want {
			t.Fatalf("days[%d].date = %q, want %q (oldest→newest, no gaps)", i, d.Date, want)
		}
	}
	if section.Days[0].BaselineAvg == nil || section.Days[0].Status == recoverytrend.StatusUnknown {
		t.Errorf("first charted day has no band (%+v); its lead-in was not fetched", section.Days[0])
	}
	if section.Baseline.HRVAvg == nil {
		t.Error("baseline.hrv_avg = null, want populated")
	}
	if section.HRV.Status == recoverytrend.StatusUnknown || section.HRV.ShortAvg == nil {
		t.Errorf("hrv = %+v, want a populated block", section.HRV)
	}
	if section.BaselineTrend.Direction == recoverytrend.TrendUnknown {
		t.Errorf("baseline_trend = %+v, want a populated block", section.BaselineTrend)
	}
	if section.Today == nil || section.Today.Date != "2026-06-17" {
		t.Errorf("today = %+v, want the 2026-06-17 row", section.Today)
	}
}

// TestRecoveryHistory_MatchesDashboardSection is the drift guard the SOW insists
// on: over the dashboard's own window this route's `recovery` object must be
// key-for-key identical to the one GET /dashboard/summary emits under
// sections.recovery for the same user. Two doors, one shape — marshaled and
// compared so they cannot diverge silently.
func TestRecoveryHistory_MatchesDashboardSection(t *testing.T) {
	r, rp, userID := newTestEnv(t)
	seedWhoopConnected(t, rp, userID)
	seedRecoveryEntries(t, rp, userID, entriesBetween(testNow, time.UTC, 60, 0, rampHRV))
	if err := rp.layout.Upsert(context.Background(), userID, SingleSection([]TileID{TileRecovery})); err != nil {
		t.Fatalf("layout upsert: %v", err)
	}

	_, data := dataEnvelope(t, r, userID, "?timezone=UTC")
	fromSummary, ok := data["recovery"]
	if !ok {
		t.Fatalf("summary emitted no recovery section; data=%v", data)
	}

	// The dashboard's window is the baseline_window_days + 1 local dates ending
	// today: 2026-05-18 .. 2026-06-17 against the pinned clock.
	fromHistory, _ := historyRecovery(t, getHistory(t, r, userID, "?timezone=UTC&since=2026-05-18&until=2026-06-17"))

	if string(fromSummary) != string(fromHistory) {
		t.Errorf("the two doors drifted\nsummary: %s\nhistory: %s", fromSummary, fromHistory)
	}
}
