package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// captured records what the test server saw, so assertions run against a real
// request built by the doList/doSearch path rather than a mocked client.
type captured struct {
	method string
	path   string
	query  string
	auth   string
	body   map[string]any
}

// newCapture spins up an httptest server that records the incoming request and
// replies with a minimal httpresp-style envelope, returning the server and a
// pointer the test reads after the call.
func newCapture(t *testing.T) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		got.auth = r.Header.Get("Authorization")
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &got.body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"memories":[]}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestDoList_PathQueryAndAuth(t *testing.T) {
	srv, got := newCapture(t)

	opts := listOptions{
		base:   srv.URL,
		token:  "tok-123",
		user:   "user-42",
		limit:  50,
		offset: 10,
	}
	var out bytes.Buffer
	if err := doList(context.Background(), opts, srv.Client(), &out); err != nil {
		t.Fatalf("doList: %v", err)
	}

	if got.method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.method)
	}
	if got.path != "/admin/memories" {
		t.Errorf("path = %q, want /admin/memories", got.path)
	}
	if got.auth != "Bearer tok-123" {
		t.Errorf("auth = %q, want Bearer tok-123", got.auth)
	}

	q, err := parseQuery(got.query)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("user_id") != "user-42" {
		t.Errorf("user_id = %q, want user-42", q.Get("user_id"))
	}
	if q.Get("limit") != "50" {
		t.Errorf("limit = %q, want 50", q.Get("limit"))
	}
	if q.Get("offset") != "10" {
		t.Errorf("offset = %q, want 10", q.Get("offset"))
	}
}

func TestDoList_OmitsUserAndAuthWhenEmpty(t *testing.T) {
	srv, got := newCapture(t)

	opts := listOptions{base: srv.URL, limit: 100, offset: 0}
	var out bytes.Buffer
	if err := doList(context.Background(), opts, srv.Client(), &out); err != nil {
		t.Fatalf("doList: %v", err)
	}

	q, err := parseQuery(got.query)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q["user_id"]; ok {
		t.Errorf("user_id should be absent, got %q", q.Get("user_id"))
	}
	if got.auth != "" {
		t.Errorf("auth should be absent, got %q", got.auth)
	}
}

func TestDoSearch_BodyAndAuth(t *testing.T) {
	srv, got := newCapture(t)

	k := 5
	opts := searchOptions{
		base:  srv.URL,
		token: "admin-jwt",
		query: "deadlift PRs",
		user:  "user-7",
		k:     &k,
	}
	var out bytes.Buffer
	if err := doSearch(context.Background(), opts, srv.Client(), &out); err != nil {
		t.Fatalf("doSearch: %v", err)
	}

	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/admin/memories/search" {
		t.Errorf("path = %q, want /admin/memories/search", got.path)
	}
	if got.auth != "Bearer admin-jwt" {
		t.Errorf("auth = %q, want Bearer admin-jwt", got.auth)
	}
	if got.body["query"] != "deadlift PRs" {
		t.Errorf("query = %v, want deadlift PRs", got.body["query"])
	}
	if got.body["user_id"] != "user-7" {
		t.Errorf("user_id = %v, want user-7", got.body["user_id"])
	}
	if got.body["k"] != float64(5) { // JSON numbers decode to float64
		t.Errorf("k = %v, want 5", got.body["k"])
	}
	if _, ok := got.body["threshold"]; ok {
		t.Errorf("threshold should be absent when unset, got %v", got.body["threshold"])
	}
}

func TestDoSearch_ExplicitZeroThresholdIsSent(t *testing.T) {
	srv, got := newCapture(t)

	zero := 0.0
	opts := searchOptions{
		base:      srv.URL,
		query:     "q",
		threshold: &zero, // operator typed --threshold 0 (full sweep)
	}
	var out bytes.Buffer
	if err := doSearch(context.Background(), opts, srv.Client(), &out); err != nil {
		t.Fatalf("doSearch: %v", err)
	}

	v, ok := got.body["threshold"]
	if !ok {
		t.Fatal("threshold should be present when explicitly set to 0")
	}
	if v != float64(0) {
		t.Errorf("threshold = %v, want 0", v)
	}
	// k unset → must be absent.
	if _, ok := got.body["k"]; ok {
		t.Errorf("k should be absent when unset, got %v", got.body["k"])
	}
}

// TestRunSearch_VisitDetectsExplicitZero exercises the flag-parsing layer end
// to end to prove the Visit-based detection (not a sentinel default) forwards
// an explicit --threshold 0 while omitting it otherwise.
func TestRunSearch_VisitDetectsExplicitZero(t *testing.T) {
	srv, got := newCapture(t)

	args := []string{"--api", srv.URL, "--query", "q", "--threshold", "0"}
	var out bytes.Buffer
	if err := runSearch(args, srv.Client(), &out); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if v, ok := got.body["threshold"]; !ok || v != float64(0) {
		t.Errorf("threshold via flags = %v (present=%v), want 0/true", v, ok)
	}

	// Now without the flag: threshold must be absent.
	srv2, got2 := newCapture(t)
	args2 := []string{"--api", srv2.URL, "--query", "q"}
	if err := runSearch(args2, srv2.Client(), &out); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if _, ok := got2.body["threshold"]; ok {
		t.Errorf("threshold should be absent without the flag, got %v", got2.body["threshold"])
	}
}

func parseQuery(raw string) (url.Values, error) {
	return url.ParseQuery(raw)
}
