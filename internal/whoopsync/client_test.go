package whoopsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSleeps_FollowsNextTokenAndSendsParams(t *testing.T) {
	var pages int
	var gotPath, gotStart, gotEnd, gotLimit, gotAuth, sawSecondToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotStart, gotEnd, gotLimit = q.Get("start"), q.Get("end"), q.Get("limit")
		w.Header().Set("Content-Type", "application/json")
		if q.Get("nextToken") == "" {
			pages++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"records":    []map[string]any{{"id": "sleep-1", "score_state": "SCORED"}},
				"next_token": "page2",
			})
			return
		}
		sawSecondToken = q.Get("nextToken")
		pages++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records":    []map[string]any{{"id": "sleep-2", "score_state": "SCORED"}},
			"next_token": "",
		})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	sleeps, err := c.Sleeps(context.Background(), "tok", start, end, 25)
	if err != nil {
		t.Fatalf("Sleeps: %v", err)
	}
	if len(sleeps) != 2 || sleeps[0].ID != "sleep-1" || sleeps[1].ID != "sleep-2" {
		t.Fatalf("sleeps = %+v, want two records in page order", sleeps)
	}
	if pages != 2 {
		t.Errorf("requested %d pages, want 2", pages)
	}
	if sawSecondToken != "page2" {
		t.Errorf("second page nextToken = %q, want page2", sawSecondToken)
	}
	if gotPath != "/v2/activity/sleep" {
		t.Errorf("path = %q, want /v2/activity/sleep", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", gotAuth)
	}
	if gotStart != "2026-06-01T00:00:00Z" || gotEnd != "2026-06-30T00:00:00Z" {
		t.Errorf("start/end = %q/%q, want RFC3339 UTC bounds", gotStart, gotEnd)
	}
	if gotLimit != "25" {
		t.Errorf("limit = %q, want 25", gotLimit)
	}
}

// TestSleeps_StopsAtMaxPages pins the same bound Recoveries/Cycles carry: a
// server that never stops handing back a next_token cannot spin the loop.
func TestSleeps_StopsAtMaxPages(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records":    []map[string]any{{"id": "sleep-x", "score_state": "SCORED"}},
			"next_token": "always-more",
		})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	sleeps, err := c.Sleeps(context.Background(), "tok", time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("Sleeps: %v", err)
	}
	if pages != maxPages {
		t.Errorf("requested %d pages, want the maxPages cap of %d", pages, maxPages)
	}
	if len(sleeps) != maxPages {
		t.Errorf("got %d records, want %d", len(sleeps), maxPages)
	}
}

// TestSleeps_DecodesFullScoredRecord is the guard SOW Open Question 3 asks for:
// the JSON tags were transcribed from WHOOP's published v2 schema, not from a
// captured live response, and a single wrong tag silently produces a column of
// nulls that reads as "WHOOP didn't score it".
func TestSleeps_DecodesFullScoredRecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"records": [{
				"id": "ff9b2f1c-9a4b-4a6f-9e1a-6f2c4d8e0a11",
				"start": "2026-03-02T22:40:00.000Z",
				"end": "2026-03-03T06:15:00.000Z",
				"timezone_offset": "-06:00",
				"nap": false,
				"score_state": "SCORED",
				"score": {
					"stage_summary": {
						"total_in_bed_time_milli": 27300000,
						"total_awake_time_milli": 1800000,
						"total_no_data_time_milli": 0,
						"total_light_sleep_time_milli": 12000000,
						"total_slow_wave_sleep_time_milli": 7500000,
						"total_rem_sleep_time_milli": 6000000,
						"sleep_cycle_count": 5,
						"disturbance_count": 9
					},
					"sleep_needed": {
						"baseline_milli": 28000000,
						"need_from_sleep_debt_milli": 1200000,
						"need_from_recent_strain_milli": 900000,
						"need_from_recent_nap_milli": -600000
					},
					"respiratory_rate": 14.7,
					"sleep_performance_percentage": 92.5,
					"sleep_consistency_percentage": 71,
					"sleep_efficiency_percentage": 88.25
				}
			}],
			"next_token": ""
		}`))
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	sleeps, err := c.Sleeps(context.Background(), "tok", time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("Sleeps: %v", err)
	}
	if len(sleeps) != 1 {
		t.Fatalf("got %d records, want 1", len(sleeps))
	}
	s := sleeps[0]
	if s.ID != "ff9b2f1c-9a4b-4a6f-9e1a-6f2c4d8e0a11" {
		t.Errorf("id = %q", s.ID)
	}
	if s.Start != "2026-03-02T22:40:00.000Z" || s.End != "2026-03-03T06:15:00.000Z" {
		t.Errorf("start/end = %q/%q", s.Start, s.End)
	}
	if s.TimezoneOffset != "-06:00" {
		t.Errorf("timezone_offset = %q, want -06:00", s.TimezoneOffset)
	}
	if s.Nap {
		t.Error("nap = true, want false")
	}
	if s.ScoreState != "SCORED" || s.Score == nil {
		t.Fatalf("score_state = %q, score = %+v", s.ScoreState, s.Score)
	}
	if s.Score.StageSummary == nil || s.Score.SleepNeeded == nil {
		t.Fatalf("stage_summary/sleep_needed missing: %+v", s.Score)
	}
	for _, tc := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"total_in_bed_time_milli", s.Score.StageSummary.TotalInBedTimeMilli, 27300000},
		{"total_awake_time_milli", s.Score.StageSummary.TotalAwakeTimeMilli, 1800000},
		{"total_no_data_time_milli", s.Score.StageSummary.TotalNoDataTimeMilli, 0},
		{"total_light_sleep_time_milli", s.Score.StageSummary.TotalLightSleepTimeMilli, 12000000},
		{"total_slow_wave_sleep_time_milli", s.Score.StageSummary.TotalSlowWaveSleepTimeMilli, 7500000},
		{"total_rem_sleep_time_milli", s.Score.StageSummary.TotalRemSleepTimeMilli, 6000000},
		{"sleep_cycle_count", s.Score.StageSummary.SleepCycleCount, 5},
		{"disturbance_count", s.Score.StageSummary.DisturbanceCount, 9},
		{"baseline_milli", s.Score.SleepNeeded.BaselineMilli, 28000000},
		{"need_from_sleep_debt_milli", s.Score.SleepNeeded.NeedFromSleepDebtMilli, 1200000},
		{"need_from_recent_strain_milli", s.Score.SleepNeeded.NeedFromRecentStrainMilli, 900000},
		{"need_from_recent_nap_milli", s.Score.SleepNeeded.NeedFromRecentNapMilli, -600000},
	} {
		if tc.got == nil {
			t.Errorf("%s decoded nil, want %d (wrong json tag?)", tc.name, tc.want)
			continue
		}
		if *tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, *tc.got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"respiratory_rate", s.Score.RespiratoryRate, 14.7},
		{"sleep_performance_percentage", s.Score.SleepPerformancePercentage, 92.5},
		{"sleep_consistency_percentage", s.Score.SleepConsistencyPercentage, 71},
		{"sleep_efficiency_percentage", s.Score.SleepEfficiencyPercentage, 88.25},
	} {
		if tc.got == nil {
			t.Errorf("%s decoded nil, want %v (wrong json tag?)", tc.name, tc.want)
			continue
		}
		if *tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, *tc.got, tc.want)
		}
	}
}

func TestSleeps_PendingHasNilScoreAndNapDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records": []map[string]any{
				{"id": "sleep-p", "start": "2026-03-03T14:00:00Z", "end": "2026-03-03T14:45:00Z",
					"timezone_offset": "-06:00", "nap": true, "score_state": "PENDING"},
			},
			"next_token": "",
		})
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	c := NewClient(srv.Client())
	sleeps, err := c.Sleeps(context.Background(), "tok", time.Now(), time.Now(), 10)
	if err != nil {
		t.Fatalf("Sleeps: %v", err)
	}
	if len(sleeps) != 1 {
		t.Fatalf("got %d records, want 1", len(sleeps))
	}
	if sleeps[0].ScoreState != "PENDING" {
		t.Errorf("score_state = %q, want PENDING", sleeps[0].ScoreState)
	}
	if sleeps[0].Score != nil {
		t.Errorf("Score = %+v, want nil", sleeps[0].Score)
	}
	if !sleeps[0].Nap {
		t.Error("nap = false, want true")
	}
}

// TestSleeps_ClassifiesStatuses pins the three shapes the sync path branches on.
// 403 gets no sentinel of its own: the scope gate in syncSleepWindow means an
// under-scoped connection never reaches the wire, so a 403 here is a genuine
// upstream surprise and belongs in the generic bucket.
func TestSleeps_ClassifiesStatuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantIs  error
		wantMsg string
	}{
		{"rate limited", http.StatusTooManyRequests, ErrRateLimited, ""},
		{"token rejected", http.StatusUnauthorized, ErrTokenRejected, ""},
		{"forbidden is generic", http.StatusForbidden, nil, "status 403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			withAPIBase(t, srv.URL)

			c := NewClient(srv.Client())
			_, err := c.Sleeps(context.Background(), "tok", time.Now(), time.Now(), 10)
			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			if tc.wantIs != nil {
				if !errors.Is(err, tc.wantIs) {
					t.Fatalf("err = %v, want %v", err, tc.wantIs)
				}
				return
			}
			if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrTokenRejected) {
				t.Fatalf("err = %v, want a generic error with no sentinel", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want it to carry %q", err, tc.wantMsg)
			}
		})
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

// Captured at init, before any test mutates the package vars via withAPIBase.
var (
	defaultWhoopAPIBase   = whoopAPIBase
	defaultWhoopRevokeURL = whoopRevokeURL
)

// WHOOP's data API lives under /developer (unlike its OAuth endpoints at the
// host root). v0.79.0 shipped without the prefix and every data call 404'd —
// this pins the production URLs so the prefix can't silently regress.
func TestProductionURLsCarryDeveloperPrefix(t *testing.T) {
	if defaultWhoopAPIBase != "https://api.prod.whoop.com/developer" {
		t.Errorf("whoopAPIBase = %q, want https://api.prod.whoop.com/developer", defaultWhoopAPIBase)
	}
	if defaultWhoopRevokeURL != "https://api.prod.whoop.com/developer/v2/user/access" {
		t.Errorf("whoopRevokeURL = %q, want https://api.prod.whoop.com/developer/v2/user/access", defaultWhoopRevokeURL)
	}
}
