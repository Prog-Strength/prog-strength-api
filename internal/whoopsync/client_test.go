package whoopsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// withAPIBase repoints the package-level WHOOP API base at base for the
// duration of the test, restoring it after.
func withAPIBase(t *testing.T, base string) {
	t.Helper()
	old := whoopAPIBase
	whoopAPIBase = base
	t.Cleanup(func() { whoopAPIBase = old })
}

func TestProfile_DecodesUserID(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":    int64(12345),
			"email":      "a@b.com",
			"first_name": "Ada",
			"last_name":  "Lovelace",
		})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	p, err := c.Profile(context.Background(), "tok-abc")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.UserID != 12345 {
		t.Errorf("user_id = %d, want 12345", p.UserID)
	}
	if gotPath != "/v2/user/profile/basic" {
		t.Errorf("path = %s", gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth = %q, want Bearer tok-abc", gotAuth)
	}
}

func TestRecoveries_FollowsNextTokenAndSendsParams(t *testing.T) {
	var pages int
	var sawLimit, sawStart, sawEnd, sawAuth bool
	var sawSecondToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Header.Get("Authorization") == "Bearer tok" {
			sawAuth = true
		}
		if q.Get("limit") == "25" {
			sawLimit = true
		}
		if q.Get("start") != "" {
			sawStart = true
		}
		if q.Get("end") != "" {
			sawEnd = true
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Get("nextToken") == "" {
			pages++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"records":    []map[string]any{{"cycle_id": 1, "sleep_id": "s1", "score_state": "SCORED"}},
				"next_token": "page2",
			})
			return
		}
		sawSecondToken = q.Get("nextToken")
		pages++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records":    []map[string]any{{"cycle_id": 2, "sleep_id": "s2", "score_state": "SCORED"}},
			"next_token": "",
		})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	recs, err := c.Recoveries(context.Background(), "tok", start, end, 25)
	if err != nil {
		t.Fatalf("Recoveries: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0].CycleID != 1 || recs[1].CycleID != 2 {
		t.Errorf("records = %+v", recs)
	}
	if pages != 2 {
		t.Errorf("requested %d pages, want 2", pages)
	}
	if sawSecondToken != "page2" {
		t.Errorf("second page nextToken = %q, want page2", sawSecondToken)
	}
	if !sawAuth {
		t.Error("bearer header not seen")
	}
	if !sawLimit || !sawStart || !sawEnd {
		t.Errorf("query params: limit=%v start=%v end=%v", sawLimit, sawStart, sawEnd)
	}
}

func TestRecoveries_PendingHasNilScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records": []map[string]any{
				{"cycle_id": 7, "sleep_id": "s7", "score_state": "PENDING"},
			},
			"next_token": "",
		})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	recs, err := c.Recoveries(context.Background(), "tok", time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("Recoveries: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0].ScoreState != "PENDING" {
		t.Errorf("score_state = %q, want PENDING", recs[0].ScoreState)
	}
	if recs[0].Score != nil {
		t.Errorf("Score = %+v, want nil", recs[0].Score)
	}
}

func TestCycles_SinglePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/cycle" {
			t.Errorf("path = %s, want /v2/cycle", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records": []map[string]any{
				{"id": 99, "start": "2026-06-01T00:00:00Z", "timezone_offset": "-08:00"},
			},
			"next_token": "",
		})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	cycles, err := c.Cycles(context.Background(), "tok", time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("Cycles: %v", err)
	}
	if len(cycles) != 1 || cycles[0].ID != 99 {
		t.Fatalf("cycles = %+v", cycles)
	}
	if cycles[0].TimezoneOffset != "-08:00" {
		t.Errorf("timezone_offset = %q", cycles[0].TimezoneOffset)
	}
}

func TestClassifyStatus_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	_, err := c.Profile(context.Background(), "tok")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err is not *RateLimitError: %v", err)
	}
	if rle.RetryAfter != "30" {
		t.Errorf("RetryAfter = %q, want 30", rle.RetryAfter)
	}
}

func TestClassifyStatus_TokenRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	_, err := c.Profile(context.Background(), "tok")
	if !errors.Is(err, ErrTokenRejected) {
		t.Errorf("err = %v, want ErrTokenRejected", err)
	}
}
