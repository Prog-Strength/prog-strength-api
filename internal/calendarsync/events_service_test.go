package calendarsync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	"github.com/Prog-Strength/prog-strength-api/internal/daterange"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
)

// eventsNow is the fixed clock every events test runs on.
var eventsNow = time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

// stubLinks is an EventLinkRepository over a fixed map, so marking is exercised
// without a database. The real lookup has its own SQLite tests.
type stubLinks struct {
	links map[string]EventLink
	err   error

	calls    int
	from, to time.Time
}

func (s *stubLinks) LinksForUser(_ context.Context, _ string, from, to time.Time) (map[string]EventLink, error) {
	s.calls++
	s.from, s.to = from, to
	if s.err != nil {
		return nil, s.err
	}
	return s.links, nil
}

// eventsHarness wires an EventsService over ephemeral SQLite connection
// storage, the REAL Google client pointed at an httptest.Server, and a stub
// link repository.
//
// The real client (rather than a fake CalendarClient) is deliberate: the
// outgoing query and the 401-vs-429 distinction are both properties of the
// HTTP layer, and a fake that returns ErrTokenRejected on demand would let the
// service pass a test that a real 429 would fail.
type eventsHarness struct {
	t     *testing.T
	svc   *EventsService
	conns calendarconn.Repository
	links *stubLinks
	srv   *httptest.Server
	now   time.Time

	mu      sync.Mutex
	queries []url.Values
	status  int
	body    string
}

func defaultEventsConfig() EventsConfig {
	return EventsConfig{Enabled: true, CacheTTL: 60 * time.Second, MaxEventsPerDay: 50}
}

// newEventsHarness builds a service whose Google calls land on a local server.
// Each id in connectedUsers gets a connected calendar connection row; pass none
// for a user who never opted in.
func newEventsHarness(t *testing.T, cfg EventsConfig, connectedUsers ...string) *eventsHarness {
	t.Helper()

	h := &eventsHarness{
		t:      t,
		links:  &stubLinks{},
		now:    eventsNow,
		status: http.StatusOK,
		body:   `{"items":[]}`,
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.queries = append(h.queries, r.URL.Query())
		status, body := h.status, h.body
		h.mu.Unlock()

		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(h.srv.Close)
	withAPIBase(t, h.srv.URL)

	database := dbtest.New(t)
	conns := calendarconn.NewSQLiteRepository(database)
	cipher, err := tokencrypt.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	enc, nonce, err := cipher.Encrypt([]byte("refresh-token"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	for _, userID := range connectedUsers {
		if upErr := conns.Upsert(context.Background(), userID, enc, nonce, "primary", CalendarEventsScope, eventsNow); upErr != nil {
			t.Fatalf("Upsert conn %s: %v", userID, upErr)
		}
	}

	svc := NewEventsService(EventsServiceDeps{
		Conns:  conns,
		Cipher: cipher,
		Client: NewGoogleCalendarClient(h.srv.Client()),
		Links:  h.links,
		Config: cfg,
		Now:    func() time.Time { return h.now },
	})
	svc.conn.tokens = fakeTokens{} // inject the fake token minter directly

	h.svc, h.conns = svc, conns
	return h
}

// respond scripts the next Google responses.
func (h *eventsHarness) respond(status int, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status, h.body = status, body
}

// requests is how many times Google was called.
func (h *eventsHarness) requests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.queries)
}

func (h *eventsHarness) lastQuery() url.Values {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.queries) == 0 {
		h.t.Fatal("expected a Google request, got none")
	}
	return h.queries[len(h.queries)-1]
}

func (h *eventsHarness) connStatus(userID string) calendarconn.Status {
	h.t.Helper()
	conn, err := h.conns.Get(context.Background(), userID)
	if err != nil {
		h.t.Fatalf("Get connection: %v", err)
	}
	return conn.Status
}

// eventsWindow resolves a request's date contract exactly as the handler does:
// through daterange, never by clock arithmetic.
func eventsWindow(t *testing.T, timezone, startDate, endDate string) EventsWindow {
	t.Helper()
	loc, err := daterange.LoadTimezone(timezone)
	if err != nil {
		t.Fatalf("LoadTimezone: %v", err)
	}
	start, _, err := daterange.DayBoundsUTC(startDate, loc)
	if err != nil {
		t.Fatalf("DayBoundsUTC(%s): %v", startDate, err)
	}
	_, end, err := daterange.DayBoundsUTC(endDate, loc)
	if err != nil {
		t.Fatalf("DayBoundsUTC(%s): %v", endDate, err)
	}
	return EventsWindow{StartUTC: start, EndUTC: end, Loc: loc, StartDate: startDate, EndDate: endDate}
}

// dayFor returns the day with the given date, failing when the window did not
// contain it.
func dayFor(t *testing.T, days []Day, date string) Day {
	t.Helper()
	for _, d := range days {
		if d.Date == date {
			return d
		}
	}
	t.Fatalf("no day %s in %v", date, dayDates(days))
	return Day{}
}

func dayDates(days []Day) []string {
	out := make([]string, 0, len(days))
	for _, d := range days {
		out = append(out, d.Date)
	}
	return out
}

func eventIDs(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func eventByID(t *testing.T, days []Day, id string) Event {
	t.Helper()
	for _, d := range days {
		for _, e := range d.Events {
			if e.ID == id {
				return e
			}
		}
	}
	t.Fatalf("no event %s in the result", id)
	return Event{}
}

// The DST pin. A week that crosses the US fall-back is 169 hours, not 168, and
// a day list built by adding 24 hours repeatedly repeats a date and drops the
// last one. Both the outgoing Google window and the grouped days are asserted
// against what daterange yields.
func TestEvents_WindowFromDaterange_DSTWeek(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	// 2026-11-01 is the US DST fall-back; the local day is 25 hours long.
	w := eventsWindow(t, "America/New_York", "2026-11-01", "2026-11-08")
	h.respond(http.StatusOK, `{"items":[
		{"id":"first","summary":"Fall back","start":{"dateTime":"2026-11-01T14:00:00Z"},"end":{"dateTime":"2026-11-01T15:00:00Z"}},
		{"id":"last","summary":"Week out","start":{"dateTime":"2026-11-08T13:00:00Z"},"end":{"dateTime":"2026-11-08T14:00:00Z"}}
	]}`)

	// The window itself: eight local days spanning 193 hours, not 192.
	if got, want := w.EndUTC.Sub(w.StartUTC), 8*24*time.Hour+time.Hour; got != want {
		t.Fatalf("window span = %v, want %v (the fall-back day is 25 hours)", got, want)
	}

	res := h.svc.Events(context.Background(), testUserID, w)
	if res.Status != EventsStatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}

	q := h.lastQuery()
	if got, want := q.Get("timeMin"), "2026-11-01T04:00:00Z"; got != want {
		t.Errorf("timeMin = %q, want %q", got, want)
	}
	if got, want := q.Get("timeMax"), "2026-11-09T05:00:00Z"; got != want {
		t.Errorf("timeMax = %q, want %q", got, want)
	}

	wantDates := []string{
		"2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04",
		"2026-11-05", "2026-11-06", "2026-11-07", "2026-11-08",
	}
	if got := dayDates(res.Days); len(got) != len(wantDates) {
		t.Fatalf("dates = %v, want %v", got, wantDates)
	}
	for i, want := range wantDates {
		if res.Days[i].Date != want {
			t.Fatalf("dates = %v, want %v", dayDates(res.Days), wantDates)
		}
	}
	if got := eventIDs(dayFor(t, res.Days, "2026-11-01").Events); len(got) != 1 || got[0] != "first" {
		t.Errorf("2026-11-01 events = %v, want [first]", got)
	}
	if got := eventIDs(dayFor(t, res.Days, "2026-11-08").Events); len(got) != 1 || got[0] != "last" {
		t.Errorf("2026-11-08 events = %v, want [last]", got)
	}
}

func TestEvents_QueryCarriesSingleEventsAndOrderBy(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-19"))
	if res.Status != EventsStatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}

	q := h.lastQuery()
	// Without singleEvents Google returns the recurring RULE rather than its
	// instances, and orderBy=startTime is rejected unless it is set.
	if got := q.Get("singleEvents"); got != "true" {
		t.Errorf("singleEvents = %q, want true", got)
	}
	if got := q.Get("orderBy"); got != "startTime" {
		t.Errorf("orderBy = %q, want startTime", got)
	}
	// The page is the per-day cap across the window's days, so the per-day cap
	// is what actually truncates.
	if got := q.Get("maxResults"); got != "400" {
		t.Errorf("maxResults = %q, want 400 (50 * 8 days)", got)
	}
}

// config.CalendarEventsConfig documents max_events_per_day = 0 as "no cap".
// The page we ask Google for has to honor that, or an operator who disabled
// the cap would get ONE event per window instead of all of them.
func TestEvents_PageSizeHonorsAnUncappedConfig(t *testing.T) {
	t.Run("no per-day cap asks for Google's whole page", func(t *testing.T) {
		cfg := defaultEventsConfig()
		cfg.MaxEventsPerDay = 0
		h := newEventsHarness(t, cfg, testUserID)
		h.respond(http.StatusOK, `{"items":[
			{"id":"e1","summary":"One","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}},
			{"id":"e2","summary":"Two","start":{"dateTime":"2026-08-12T14:00:00Z"},"end":{"dateTime":"2026-08-12T15:00:00Z"}}
		]}`)

		res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

		if got := h.lastQuery().Get("maxResults"); got != "2500" {
			t.Errorf("maxResults = %q, want 2500 (Google's page maximum)", got)
		}
		day := dayFor(t, res.Days, "2026-08-12")
		if len(day.Events) != 2 || day.Truncated != 0 {
			t.Errorf("day = %+v, want both events and no truncation", day)
		}
	})

	t.Run("a window wider than Google's page clamps", func(t *testing.T) {
		cfg := defaultEventsConfig()
		cfg.MaxEventsPerDay = 50
		h := newEventsHarness(t, cfg, testUserID)

		// 60 days * 50 would be 3000, past what events.list will return.
		h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-10-10"))

		if got := h.lastQuery().Get("maxResults"); got != "2500" {
			t.Errorf("maxResults = %q, want 2500", got)
		}
	})
}

func TestEvents_DropsDeclinedKeepsAcceptedAndNeedsAction(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.respond(http.StatusOK, `{"items":[
		{"id":"declined","summary":"Declined","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"},
			"attendees":[{"self":true,"responseStatus":"declined"}]},
		{"id":"accepted","summary":"Accepted","start":{"dateTime":"2026-08-12T14:00:00Z"},"end":{"dateTime":"2026-08-12T15:00:00Z"},
			"attendees":[{"self":true,"responseStatus":"accepted"}]},
		{"id":"needs_action","summary":"Unanswered","start":{"dateTime":"2026-08-12T15:00:00Z"},"end":{"dateTime":"2026-08-12T16:00:00Z"},
			"attendees":[{"self":true,"responseStatus":"needsAction"}]},
		{"id":"someone_else_declined","summary":"Theirs","start":{"dateTime":"2026-08-12T16:00:00Z"},"end":{"dateTime":"2026-08-12T17:00:00Z"},
			"attendees":[{"self":false,"responseStatus":"declined"}]}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	got := eventIDs(dayFor(t, res.Days, "2026-08-12").Events)
	want := []string{"accepted", "needs_action", "someone_else_declined"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestEvents_NoSummaryRendersBusy(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.respond(http.StatusOK, `{"items":[
		{"id":"private","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	if got := eventByID(t, res.Days, "private").Title; got != "Busy" {
		t.Errorf("title = %q, want %q", got, "Busy")
	}
}

func TestEvents_MultiDayAllDaySpansEveryDay(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.respond(http.StatusOK, `{"items":[
		{"id":"vacation","summary":"Vacation","start":{"date":"2026-08-14"},"end":{"date":"2026-08-16"}},
		{"id":"standup","summary":"Standup","start":{"dateTime":"2026-08-13T13:00:00Z"},"end":{"dateTime":"2026-08-13T13:15:00Z"}}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "America/New_York", "2026-08-12", "2026-08-17"))

	// Google's end.date is EXCLUSIVE: a 14th -> 16th event covers 14 and 15.
	for _, date := range []string{"2026-08-14", "2026-08-15"} {
		if got := eventIDs(dayFor(t, res.Days, date).Events); len(got) != 1 || got[0] != "vacation" {
			t.Errorf("%s events = %v, want [vacation]", date, got)
		}
	}
	if got := eventIDs(dayFor(t, res.Days, "2026-08-16").Events); len(got) != 0 {
		t.Errorf("2026-08-16 events = %v, want none (Google's end.date is exclusive)", got)
	}

	// An all-day row carries the local day's bounds in UTC.
	vacation := eventByID(t, res.Days, "vacation")
	if !vacation.AllDay {
		t.Error("vacation must be marked all-day")
	}
	if got, want := vacation.Start, time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("all-day start = %v, want %v", got, want)
	}
	if got, want := vacation.End, time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("all-day end = %v, want %v", got, want)
	}

	// A timed event appears only on its start day.
	if got := eventIDs(dayFor(t, res.Days, "2026-08-13").Events); len(got) != 1 || got[0] != "standup" {
		t.Errorf("2026-08-13 events = %v, want [standup]", got)
	}
	if got := eventIDs(dayFor(t, res.Days, "2026-08-12").Events); len(got) != 0 {
		t.Errorf("2026-08-12 events = %v, want none", got)
	}
}

func TestEvents_MarksOurOwnByID(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.links.links = map[string]EventLink{
		"g_pw1":  {Kind: LinkKindPlannedWorkout, ID: "pw_1"},
		"g_act1": {Kind: LinkKindActivity, ID: "act_1"},
	}
	h.respond(http.StatusOK, `{"items":[
		{"id":"g_pw1","summary":"Upper Body Push","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}},
		{"id":"g_act1","summary":"Morning Run","start":{"dateTime":"2026-08-12T14:00:00Z"},"end":{"dateTime":"2026-08-12T15:00:00Z"}},
		{"id":"g_theirs","summary":"Dentist","start":{"dateTime":"2026-08-12T15:00:00Z"},"end":{"dateTime":"2026-08-12T16:00:00Z"}}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	plan := eventByID(t, res.Days, "g_pw1")
	if plan.Source != EventSourceProgStrength || plan.Link == nil ||
		*plan.Link != (EventLink{Kind: LinkKindPlannedWorkout, ID: "pw_1"}) {
		t.Errorf("planned workout event = %+v, want prog_strength + planned_workout link", plan)
	}
	act := eventByID(t, res.Days, "g_act1")
	if act.Source != EventSourceProgStrength || act.Link == nil ||
		*act.Link != (EventLink{Kind: LinkKindActivity, ID: "act_1"}) {
		t.Errorf("activity event = %+v, want prog_strength + activity link", act)
	}
	theirs := eventByID(t, res.Days, "g_theirs")
	if theirs.Source != EventSourceGoogle || theirs.Link != nil {
		t.Errorf("unknown event = %+v, want google with no link", theirs)
	}

	// The link lookup is bounded by the request's own window.
	if !h.links.from.Equal(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)) ||
		!h.links.to.Equal(time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("link window = [%v, %v), want the request window", h.links.from, h.links.to)
	}
}

// The pin against title matching. Marking is an id-set lookup: an event that
// merely SHARES a planned workout's title is somebody else's event, and
// deep-linking it would send the user to a workout that is not on their
// calendar entry at all.
func TestEvents_TitleMatchDoesNotMark(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.links.links = map[string]EventLink{
		"g_pw1": {Kind: LinkKindPlannedWorkout, ID: "pw_1"},
	}
	h.respond(http.StatusOK, `{"items":[
		{"id":"g_pw1","summary":"Upper Body Push","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}},
		{"id":"g_stranger","summary":"Upper Body Push","start":{"dateTime":"2026-08-12T17:00:00Z"},"end":{"dateTime":"2026-08-12T18:00:00Z"}}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	stranger := eventByID(t, res.Days, "g_stranger")
	if stranger.Source != EventSourceGoogle {
		t.Errorf("source = %q, want google for an identically titled event with an unknown id", stranger.Source)
	}
	if stranger.Link != nil {
		t.Errorf("link = %+v, want none", *stranger.Link)
	}
	if ours := eventByID(t, res.Days, "g_pw1"); ours.Source != EventSourceProgStrength {
		t.Errorf("the id-matched event lost its mark: %+v", ours)
	}
}

func TestEvents_DaysAreDense(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "America/New_York", "2026-08-12", "2026-08-14"))

	want := []string{"2026-08-12", "2026-08-13", "2026-08-14"}
	if got := dayDates(res.Days); len(got) != len(want) {
		t.Fatalf("dates = %v, want %v", got, want)
	}
	for i, date := range want {
		if res.Days[i].Date != date {
			t.Fatalf("dates = %v, want %v", dayDates(res.Days), want)
		}
		if res.Days[i].Events == nil {
			t.Errorf("%s events = nil, want an empty slice (never null on the wire)", date)
		}
		if len(res.Days[i].Events) != 0 {
			t.Errorf("%s events = %v, want none", date, eventIDs(res.Days[i].Events))
		}
	}
}

func TestEvents_TruncatesAndReports(t *testing.T) {
	cfg := defaultEventsConfig()
	cfg.MaxEventsPerDay = 2
	h := newEventsHarness(t, cfg, testUserID)
	h.respond(http.StatusOK, `{"items":[
		{"id":"e1","summary":"One","start":{"dateTime":"2026-08-12T09:00:00Z"},"end":{"dateTime":"2026-08-12T10:00:00Z"}},
		{"id":"e2","summary":"Two","start":{"dateTime":"2026-08-12T10:00:00Z"},"end":{"dateTime":"2026-08-12T11:00:00Z"}},
		{"id":"e3","summary":"Three","start":{"dateTime":"2026-08-12T11:00:00Z"},"end":{"dateTime":"2026-08-12T12:00:00Z"}},
		{"id":"e4","summary":"Four","start":{"dateTime":"2026-08-12T12:00:00Z"},"end":{"dateTime":"2026-08-12T13:00:00Z"}},
		{"id":"e5","summary":"Five","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	day := dayFor(t, res.Days, "2026-08-12")
	if len(day.Events) != 2 {
		t.Fatalf("events = %v, want 2", eventIDs(day.Events))
	}
	if day.Truncated != 3 {
		t.Errorf("truncated = %d, want 3", day.Truncated)
	}
	// The cap slices off the END of the sorted day, so the earliest survive.
	if got := eventIDs(day.Events); got[0] != "e1" || got[1] != "e2" {
		t.Errorf("kept %v, want the two earliest", got)
	}
}

// All-day rows are pinned above timed ones, and the truncation cap slices off
// the end — so this ordering decides what a capped day keeps.
func TestEvents_AllDayEventsSortFirst(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.respond(http.StatusOK, `{"items":[
		{"id":"early","summary":"Early","start":{"dateTime":"2026-08-12T09:00:00Z"},"end":{"dateTime":"2026-08-12T10:00:00Z"}},
		{"id":"holiday","summary":"Holiday","start":{"date":"2026-08-12"},"end":{"date":"2026-08-13"}},
		{"id":"late","summary":"Late","start":{"dateTime":"2026-08-12T20:00:00Z"},"end":{"dateTime":"2026-08-12T21:00:00Z"}}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	want := []string{"holiday", "early", "late"}
	got := eventIDs(dayFor(t, res.Days, "2026-08-12").Events)
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestEvents_401RevokesConnectionAndReportsReconnect(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.respond(http.StatusUnauthorized, "")

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	if res.Status != EventsStatusReconnectNeeded {
		t.Errorf("status = %q, want reconnect_needed", res.Status)
	}
	if got := h.connStatus(testUserID); got != calendarconn.StatusRevoked {
		t.Errorf("connection status = %q, want revoked", got)
	}
}

// A 429 is Google rate-limiting, not a revoked grant. Flipping the connection
// over one would prompt a user whose authorization is perfectly good to
// re-consent — and, because the write path shares that connection row, would
// also stop their planned-workout sync. The stored status is asserted directly
// rather than "SetStatus was not called", so this survives a refactor of how
// the flip happens.
func TestEvents_429LeavesConnectionUntouched(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.respond(http.StatusTooManyRequests, "")

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	if res.Status != EventsStatusUnavailable {
		t.Errorf("status = %q, want unavailable", res.Status)
	}
	if got := h.connStatus(testUserID); got != calendarconn.StatusConnected {
		t.Errorf("connection status = %q, want connected — a rate limit is not a revoked grant", got)
	}
}

func TestEvents_5xxIsUnavailableAndLeavesConnection(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		h := newEventsHarness(t, defaultEventsConfig(), testUserID)
		h.respond(http.StatusInternalServerError, "")

		res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

		if res.Status != EventsStatusUnavailable {
			t.Errorf("status = %q, want unavailable", res.Status)
		}
		if got := h.connStatus(testUserID); got != calendarconn.StatusConnected {
			t.Errorf("connection status = %q, want connected", got)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		h := newEventsHarness(t, defaultEventsConfig(), testUserID)
		h.srv.Close() // nothing is listening any more

		res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

		if res.Status != EventsStatusUnavailable {
			t.Errorf("status = %q, want unavailable", res.Status)
		}
		if got := h.connStatus(testUserID); got != calendarconn.StatusConnected {
			t.Errorf("connection status = %q, want connected", got)
		}
	})
}

func TestEvents_NoConnectionRowMakesNoGoogleCall(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig()) // nobody connected

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	if res.Status != EventsStatusNotConnected {
		t.Errorf("status = %q, want not_connected", res.Status)
	}
	if res.Days != nil {
		t.Errorf("days = %v, want none", dayDates(res.Days))
	}
	if h.requests() != 0 {
		t.Errorf("Google was called %d times for a user who never connected", h.requests())
	}
}

// A revoked connection is the write path's own signal that the grant is gone;
// the reader must not spend a Google call rediscovering it.
func TestEvents_RevokedConnectionMakesNoGoogleCall(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	if err := h.conns.SetStatus(context.Background(), testUserID, calendarconn.StatusRevoked, eventsNow); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	if res.Status != EventsStatusReconnectNeeded {
		t.Errorf("status = %q, want reconnect_needed", res.Status)
	}
	if h.requests() != 0 {
		t.Errorf("Google was called %d times for a revoked connection", h.requests())
	}
}

func TestEvents_DisabledMakesNoGoogleCall(t *testing.T) {
	cfg := defaultEventsConfig()
	cfg.Enabled = false
	h := newEventsHarness(t, cfg, testUserID)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	if res.Status != EventsStatusDisabled {
		t.Errorf("status = %q, want disabled", res.Status)
	}
	if h.requests() != 0 {
		t.Errorf("Google was called %d times with the read kill switch off", h.requests())
	}
}

func TestEvents_CacheServesInsideTTLAndRefetchesAfter(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.respond(http.StatusOK, `{"items":[
		{"id":"e1","summary":"One","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}}
	]}`)
	w := eventsWindow(t, "UTC", "2026-08-12", "2026-08-12")

	first := h.svc.Events(context.Background(), testUserID, w)
	second := h.svc.Events(context.Background(), testUserID, w)
	if first.Status != EventsStatusOK || second.Status != EventsStatusOK {
		t.Fatalf("statuses = %q/%q, want ok", first.Status, second.Status)
	}
	if h.requests() != 1 {
		t.Fatalf("Google calls = %d, want 1 inside the TTL", h.requests())
	}
	if got := eventIDs(dayFor(t, second.Days, "2026-08-12").Events); len(got) != 1 || got[0] != "e1" {
		t.Errorf("cached events = %v, want [e1]", got)
	}

	h.now = h.now.Add(61 * time.Second)
	if res := h.svc.Events(context.Background(), testUserID, w); res.Status != EventsStatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if h.requests() != 2 {
		t.Errorf("Google calls = %d, want 2 once the TTL lapsed", h.requests())
	}
}

func TestEvents_CacheIsPerUser(t *testing.T) {
	const otherUserID = "user-2"
	h := newEventsHarness(t, defaultEventsConfig(), testUserID, otherUserID)
	h.respond(http.StatusOK, `{"items":[
		{"id":"e1","summary":"One","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}}
	]}`)
	w := eventsWindow(t, "UTC", "2026-08-12", "2026-08-12")

	first := h.svc.Events(context.Background(), testUserID, w)
	second := h.svc.Events(context.Background(), otherUserID, w)

	if first.Status != EventsStatusOK || second.Status != EventsStatusOK {
		t.Fatalf("statuses = %q/%q, want ok", first.Status, second.Status)
	}
	if h.requests() != 2 {
		t.Errorf("Google calls = %d, want 2 — two users must never share a cache entry", h.requests())
	}
	if got := eventIDs(dayFor(t, second.Days, "2026-08-12").Events); len(got) != 1 {
		t.Errorf("second user's events = %v, want their own fetch", got)
	}
}

// The events are the payload; the provenance mark is a garnish. A link lookup
// that fails must degrade to an unmarked tile, not to no tile.
func TestEvents_LinkLookupFailureStillReturnsEvents(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	h.links.err = errors.New("database is gone")
	h.respond(http.StatusOK, `{"items":[
		{"id":"e1","summary":"One","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"}}
	]}`)

	res := h.svc.Events(context.Background(), testUserID, eventsWindow(t, "UTC", "2026-08-12", "2026-08-12"))

	if res.Status != EventsStatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	ev := eventByID(t, res.Days, "e1")
	if ev.Source != EventSourceGoogle || ev.Link != nil {
		t.Errorf("event = %+v, want an unmarked event", ev)
	}
}

// Every read is counted, and the cache label is what makes the Google-call rate
// interpretable — so a hit must not be recorded as a miss.
func TestEvents_RecordsReadMetrics(t *testing.T) {
	h := newEventsHarness(t, defaultEventsConfig(), testUserID)
	w := eventsWindow(t, "UTC", "2026-08-12", "2026-08-12")

	missBefore := testutil.ToFloat64(eventReadsTotal.WithLabelValues(string(EventsStatusOK), cacheMiss))
	hitBefore := testutil.ToFloat64(eventReadsTotal.WithLabelValues(string(EventsStatusOK), cacheHit))

	h.svc.Events(context.Background(), testUserID, w)
	h.svc.Events(context.Background(), testUserID, w)

	if got := testutil.ToFloat64(eventReadsTotal.WithLabelValues(string(EventsStatusOK), cacheMiss)); got != missBefore+1 {
		t.Errorf("ok/miss = %v, want %v", got, missBefore+1)
	}
	if got := testutil.ToFloat64(eventReadsTotal.WithLabelValues(string(EventsStatusOK), cacheHit)); got != hitBefore+1 {
		t.Errorf("ok/hit = %v, want %v", got, hitBefore+1)
	}
}

// The pin that this feature never silently grows a scope. calendar.events is
// read AND write on events, which is why the tile ships without re-consent;
// reading WHICH calendars a user has would need calendar.readonly and a
// re-consent for every existing connected user.
func TestEvents_ReaderRequestsOnlyTheEventsScope(t *testing.T) {
	cfg := NewCalendarConfig("id", "secret", "https://example.test/cb")
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != CalendarEventsScope {
		t.Fatalf("scopes = %v, want exactly [%s]", cfg.Scopes, CalendarEventsScope)
	}
}
