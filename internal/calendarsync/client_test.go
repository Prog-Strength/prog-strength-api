package calendarsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
