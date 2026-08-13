package calendarsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func testEvent() GoogleEvent {
	return GoogleEvent{
		Summary:     "Push Day",
		Description: "Reserved training slot.",
		StartUTC:    time.Date(2026, 6, 20, 17, 0, 0, 0, time.UTC),
		EndUTC:      time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC),
		Timezone:    "America/New_York",
	}
}

// withAPIBase repoints the package-level calendar API base at srv for the
// duration of the test, restoring it after.
func withAPIBase(t *testing.T, base string) {
	t.Helper()
	old := calendarAPIBase
	calendarAPIBase = base
	t.Cleanup(func() { calendarAPIBase = old })
}

func TestInsertEvent_RequestShapeAndID(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "evt-123"})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewGoogleCalendarClient(srv.Client())
	id, err := c.InsertEvent(context.Background(), "tok-abc", "primary", testEvent())
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if id != "evt-123" {
		t.Errorf("id = %q want evt-123", id)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s want POST", gotMethod)
	}
	if gotPath != "/calendars/primary/events" {
		t.Errorf("path = %s", gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"summary":"Push Day"`) {
		t.Errorf("body missing summary: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"timeZone":"America/New_York"`) {
		t.Errorf("body missing timezone: %s", gotBody)
	}
}

func TestInsertEvent_EscapesCalendarID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "x"})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewGoogleCalendarClient(srv.Client())
	// A path-reserved char (slash) in the id must be percent-encoded so it
	// can't break out of the {calendarID} segment.
	if _, err := c.InsertEvent(context.Background(), "t", "team/cal", testEvent()); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if !strings.Contains(gotPath, "team%2Fcal") {
		t.Errorf("calendar id not escaped: %s", gotPath)
	}
}

func TestPatchEvent_RequestShape(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewGoogleCalendarClient(srv.Client())
	if err := c.PatchEvent(context.Background(), "tok", "primary", "evt-9", testEvent()); err != nil {
		t.Fatalf("PatchEvent: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %s want PATCH", gotMethod)
	}
	if gotPath != "/calendars/primary/events/evt-9" {
		t.Errorf("path = %s", gotPath)
	}
}

func TestDeleteEvent_RequestShape(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewGoogleCalendarClient(srv.Client())
	if err := c.DeleteEvent(context.Background(), "tok", "primary", "evt-9"); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s want DELETE", gotMethod)
	}
	if gotPath != "/calendars/primary/events/evt-9" {
		t.Errorf("path = %s", gotPath)
	}
}

func TestStatusSentinels(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, ErrEventGone},
		{http.StatusGone, ErrEventGone},
		{http.StatusUnauthorized, ErrTokenRejected},
		{http.StatusForbidden, ErrTokenRejected},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		c := NewGoogleCalendarClient(srv.Client())
		withAPIBase(t, srv.URL)

		err := c.PatchEvent(context.Background(), "tok", "primary", "evt", testEvent())
		if !errors.Is(err, tc.want) {
			t.Errorf("patch status %d: err = %v, want %v", tc.status, err, tc.want)
		}
		// Delete maps the same way.
		err = c.DeleteEvent(context.Background(), "tok", "primary", "evt")
		if !errors.Is(err, tc.want) {
			t.Errorf("delete status %d: err = %v, want %v", tc.status, err, tc.want)
		}
		srv.Close()
	}
}

func TestInsertEvent_GenericErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewGoogleCalendarClient(srv.Client())
	_, err := c.InsertEvent(context.Background(), "tok", "primary", testEvent())
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if errors.Is(err, ErrEventGone) || errors.Is(err, ErrTokenRejected) {
		t.Errorf("500 should be a generic error, got %v", err)
	}
}

func TestListEvents_QueryParams(t *testing.T) {
	var got url.Values
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewGoogleCalendarClient(srv.Client())
	start := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 18, 4, 0, 0, 0, time.UTC)
	if _, err := c.ListEvents(context.Background(), "tok", "primary", start, end, 400); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
	// singleEvents=true is load-bearing: without it Google returns the
	// recurring RULE, not its instances, and a daily standup either vanishes
	// or lands on the day the series began. orderBy=startTime is rejected by
	// the API unless singleEvents is set, so the two travel together.
	for k, want := range map[string]string{
		"timeMin":      start.Format(time.RFC3339),
		"timeMax":      end.Format(time.RFC3339),
		"singleEvents": "true",
		"orderBy":      "startTime",
		"showDeleted":  "false",
		"maxResults":   "400",
	} {
		if got.Get(k) != want {
			t.Errorf("query %s = %q, want %q", k, got.Get(k), want)
		}
	}
}

func TestListEvents_ParsesTimedAndAllDay(t *testing.T) {
	body := `{"items":[
		{"id":"t1","summary":"Standup","start":{"dateTime":"2026-08-12T09:00:00-04:00"},"end":{"dateTime":"2026-08-12T09:15:00-04:00"}},
		{"id":"a1","summary":"Holiday","start":{"date":"2026-08-14"},"end":{"date":"2026-08-16"}},
		{"id":"n1","start":{"dateTime":"2026-08-12T13:00:00-04:00"},"end":{"dateTime":"2026-08-12T14:00:00-04:00"}}
	]}`
	events := listEventsWithBody(t, body)

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].AllDay || events[0].Summary != "Standup" {
		t.Errorf("timed event parsed wrong: %+v", events[0])
	}
	if !events[0].Start.Equal(time.Date(2026, 8, 12, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 13:00Z", events[0].Start)
	}
	if !events[1].AllDay || events[1].StartDate != "2026-08-14" || events[1].EndDate != "2026-08-16" {
		t.Errorf("all-day event parsed wrong: %+v", events[1])
	}
	// A missing summary is preserved as empty — rendering "Busy" is the
	// service's decision, not the transport's.
	if events[2].Summary != "" {
		t.Errorf("summary = %q, want empty", events[2].Summary)
	}
}

// TestListEvents_AllDayEndFallback pins the all-day branch's degradation. An
// EndDate of "" is worse than a wrong one: Google's end.date is EXCLUSIVE, so
// the day expansion downstream parses it, and "" makes a holiday either vanish
// or poison the window.
func TestListEvents_AllDayEndFallback(t *testing.T) {
	body := `{"items":[
		{"id":"a1","summary":"No end at all","start":{"date":"2026-08-14"}},
		{"id":"a2","summary":"Start date, end dateTime","start":{"date":"2026-08-20"},"end":{"dateTime":"2026-08-20T17:00:00Z"}},
		{"id":"a3","summary":"Unparseable end","start":{"date":"2026-08-25"},"end":{"date":"not-a-date"}},
		{"id":"bad","summary":"Unparseable start","start":{"date":"nope"},"end":{"date":"2026-08-26"}}
	]}`
	events := listEventsWithBody(t, body)

	// The unparseable START is dropped: there is no day to file it under.
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (an unparseable start.date must be dropped)", len(events))
	}
	for i, want := range []struct{ start, end string }{
		{"2026-08-14", "2026-08-15"},
		{"2026-08-20", "2026-08-21"},
		{"2026-08-25", "2026-08-26"},
	} {
		ev := events[i]
		if !ev.AllDay || ev.StartDate != want.start || ev.EndDate != want.end {
			t.Errorf("event %d = %+v, want all-day %s→%s (exclusive)", i, ev, want.start, want.end)
		}
	}
}

func TestListEvents_DeclinedByTheUser(t *testing.T) {
	body := `{"items":[
		{"id":"d1","summary":"Declined","start":{"dateTime":"2026-08-12T09:00:00Z"},"end":{"dateTime":"2026-08-12T10:00:00Z"},
		 "attendees":[{"self":true,"responseStatus":"declined"},{"responseStatus":"accepted"}]},
		{"id":"a2","summary":"Accepted","start":{"dateTime":"2026-08-12T11:00:00Z"},"end":{"dateTime":"2026-08-12T12:00:00Z"},
		 "attendees":[{"self":true,"responseStatus":"accepted"}]},
		{"id":"o1","summary":"Someone else declined","start":{"dateTime":"2026-08-12T13:00:00Z"},"end":{"dateTime":"2026-08-12T14:00:00Z"},
		 "attendees":[{"responseStatus":"declined"}]}
	]}`
	events := listEventsWithBody(t, body)

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if !events[0].Declined {
		t.Error("the user's own declined attendance must set Declined")
	}
	if events[1].Declined {
		t.Error("an accepted event must not be Declined")
	}
	if events[2].Declined {
		t.Error("ANOTHER attendee declining is not the user declining")
	}
}

func TestListEvents_StatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrTokenRejected},
		{http.StatusForbidden, ErrTokenRejected},
		{http.StatusNotFound, ErrEventGone},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		withAPIBase(t, srv.URL)

		_, err := NewGoogleCalendarClient(srv.Client()).
			ListEvents(context.Background(), "tok", "primary", time.Now(), time.Now(), 10)
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
		srv.Close()
	}
}

// listEventsWithBody stands up a server returning body and returns the parsed
// events.
func listEventsWithBody(t *testing.T, body string) []ListedEvent {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	withAPIBase(t, srv.URL)

	events, err := NewGoogleCalendarClient(srv.Client()).
		ListEvents(context.Background(), "tok", "primary", time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return events
}

// TestTokenSource_MintsAndCaches points the oauth2 config's token endpoint at a
// fake server and asserts the TokenSource mints an access token and caches it
// (a second call within validity does NOT re-hit the endpoint).
func TestTokenSource_MintsAndCaches(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-xyz",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	cfg := &oauth2.Config{
		ClientID:     "cid",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
	}
	ts := NewTokenSource(cfg, srv.Client(), func() time.Time { return time.Unix(1000, 0) })

	tok, err := ts.Token(context.Background(), "user-1", "refresh-1")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "access-xyz" {
		t.Errorf("token = %q", tok)
	}
	if _, err := ts.Token(context.Background(), "user-1", "refresh-1"); err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if hits != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (cache miss only once)", hits)
	}
}
