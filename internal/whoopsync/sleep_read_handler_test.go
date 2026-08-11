package whoopsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/whoopsleep"
)

// sleepReadEnvelope mirrors the httpresp success shape with the sleep list DTO
// typed, so tests assert on the exact snake_case wire contract.
type sleepReadEnvelope struct {
	Message string       `json:"message"`
	Data    sleepListDTO `json:"data"`
}

// newSleepDeps builds handlerDeps over an ephemeral DB with a users row seeded
// per id: user_whoop_sleep has a real foreign key to users, so a sleep upsert
// fails without one.
func newSleepDeps(t *testing.T, userIDs ...string) handlerDeps {
	t.Helper()
	conns, rec, sleep := newRepos(t, userIDs...)
	return handlerDeps{conns: conns, rec: rec, sleep: sleep, cipher: handlerCipher(t)}
}

func decodeSleep(t *testing.T, rec *httptest.ResponseRecorder) sleepListDTO {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env sleepReadEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return env.Data
}

func i64p(v int64) *int64     { return &v }
func f64p(v float64) *float64 { return &v }

// seedSleep upserts one sleep record, failing the test on error.
func seedSleep(t *testing.T, repo whoopsleep.Repository, e whoopsleep.Entry) {
	t.Helper()
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	if err := repo.Upsert(context.Background(), e, now); err != nil {
		t.Fatalf("seed sleep %s/%s: %v", e.UserID, e.WhoopSleepID, err)
	}
}

func TestGetSleepReturnsOrderedRowsWithNapsAndMilliseconds(t *testing.T) {
	d := newSleepDeps(t, "user-1", "user-2")

	seedSleep(t, d.sleep, whoopsleep.Entry{
		UserID: "user-1", WhoopSleepID: "s-10", Date: "2026-06-10",
		StartedAt: "2026-06-09T22:40:00Z", EndedAt: "2026-06-10T06:15:00Z",
		TimezoneOffset: "-06:00", ScoreState: "SCORED",
		InBedMilli: i64p(27300000), LightSleepMilli: i64p(14000000),
		SlowWaveSleepMilli: i64p(6000000), REMSleepMilli: i64p(5000000),
		PerformancePct: f64p(88), RespiratoryRate: f64p(15.2),
	})
	// A nap on the same date as the night below: both come back, `is_nap` is
	// what lets a client tell them apart.
	seedSleep(t, d.sleep, whoopsleep.Entry{
		UserID: "user-1", WhoopSleepID: "s-11-nap", Date: "2026-06-11", IsNap: true,
		StartedAt: "2026-06-11T13:00:00Z", EndedAt: "2026-06-11T13:40:00Z",
		TimezoneOffset: "-06:00", ScoreState: "SCORED",
		InBedMilli: i64p(2400000),
	})
	seedSleep(t, d.sleep, whoopsleep.Entry{
		UserID: "user-1", WhoopSleepID: "s-11", Date: "2026-06-11",
		StartedAt: "2026-06-10T23:00:00Z", EndedAt: "2026-06-11T07:00:00Z",
		TimezoneOffset: "-06:00", ScoreState: "SCORED",
		InBedMilli: i64p(28800000),
	})
	// Outside the until bound, and another user's row on an in-window date.
	seedSleep(t, d.sleep, whoopsleep.Entry{
		UserID: "user-1", WhoopSleepID: "s-20", Date: "2026-06-20",
		StartedAt: "2026-06-19T23:00:00Z", EndedAt: "2026-06-20T07:00:00Z",
		TimezoneOffset: "-06:00", ScoreState: "SCORED",
	})
	seedSleep(t, d.sleep, whoopsleep.Entry{
		UserID: "user-2", WhoopSleepID: "s-other", Date: "2026-06-11",
		StartedAt: "2026-06-10T23:00:00Z", EndedAt: "2026-06-11T07:00:00Z",
		TimezoneOffset: "-06:00", ScoreState: "SCORED",
	})

	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/sleep?since=2026-06-10&until=2026-06-11&timezone=America/Denver")
	got := decodeSleep(t, rec).Sleep

	if len(got) != 3 {
		t.Fatalf("expected 3 rows in window, got %d: %+v", len(got), got)
	}
	// date DESC, then ended_at DESC: the nap ended after the night it shares a
	// date with, so it sorts first.
	wantIDs := []string{"s-11-nap", "s-11", "s-10"}
	for i, want := range wantIDs {
		if got[i].WhoopSleepID != want {
			t.Errorf("row %d whoop_sleep_id = %q, want %q", i, got[i].WhoopSleepID, want)
		}
	}
	if !got[0].IsNap {
		t.Error("the nap row should carry is_nap=true")
	}
	if got[1].IsNap {
		t.Error("the night row should carry is_nap=false")
	}
	// Durations pass through as milliseconds, unconverted.
	night := got[2]
	if night.InBedMilli == nil || *night.InBedMilli != 27300000 {
		t.Errorf("in_bed_milli = %v, want 27300000", night.InBedMilli)
	}
	if night.RemSleepMilli == nil || *night.RemSleepMilli != 5000000 {
		t.Errorf("rem_sleep_milli = %v, want 5000000", night.RemSleepMilli)
	}
	if night.PerformancePct == nil || *night.PerformancePct != 88 {
		t.Errorf("performance_pct = %v, want 88", night.PerformancePct)
	}
	if night.Date != "2026-06-10" || night.StartedAt != "2026-06-09T22:40:00Z" || night.TimezoneOffset != "-06:00" {
		t.Errorf("row identity fields wrong: %+v", night)
	}
}

func TestGetSleepEmptyWhenNoRows(t *testing.T) {
	d := newSleepDeps(t, "user-1")
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/sleep?timezone=UTC")
	got := decodeSleep(t, rec)
	if got.Sleep == nil {
		t.Fatal("sleep should be an empty array, not null")
	}
	if len(got.Sleep) != 0 {
		t.Errorf("expected empty list, got %+v", got.Sleep)
	}
	if !strings.Contains(rec.Body.String(), `"sleep":[]`) {
		t.Errorf("body should carry an empty array literal; got %s", rec.Body.String())
	}
}

// An UNSCORABLE/PENDING record has a start, an end, and nothing else. Every
// score field must serialize as JSON null — 0 is a claim about the night.
func TestGetSleepUnscoredRowSerializesNulls(t *testing.T) {
	d := newSleepDeps(t, "user-1")
	seedSleep(t, d.sleep, whoopsleep.Entry{
		UserID: "user-1", WhoopSleepID: "s-pending", Date: "2026-06-12",
		StartedAt: "2026-06-11T23:00:00Z", EndedAt: "2026-06-12T07:00:00Z",
		TimezoneOffset: "-06:00", ScoreState: "PENDING",
	})

	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/sleep?timezone=UTC")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var env struct {
		Data struct {
			Sleep []map[string]json.RawMessage `json:"sleep"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(env.Data.Sleep) != 1 {
		t.Fatalf("expected 1 row, got %d", len(env.Data.Sleep))
	}
	row := env.Data.Sleep[0]
	nullable := []string{
		"in_bed_milli", "awake_milli", "no_data_milli", "light_sleep_milli",
		"slow_wave_sleep_milli", "rem_sleep_milli", "sleep_cycle_count",
		"disturbance_count", "need_baseline_milli", "need_from_sleep_debt_milli",
		"need_from_strain_milli", "need_from_nap_milli", "respiratory_rate",
		"performance_pct", "consistency_pct", "efficiency_pct",
	}
	for _, key := range nullable {
		raw, ok := row[key]
		if !ok {
			t.Errorf("key %q absent, want present with a null value", key)
			continue
		}
		if string(raw) != "null" {
			t.Errorf("%s = %s, want null", key, raw)
		}
	}
	// is_nap is present on every object so a client can filter without guessing.
	if raw, ok := row["is_nap"]; !ok || string(raw) != "false" {
		t.Errorf("is_nap = %s (present=%v), want false", raw, ok)
	}
	if raw := row["score_state"]; string(raw) != `"PENDING"` {
		t.Errorf("score_state = %s, want \"PENDING\"", raw)
	}
}

// timezone is REQUIRED here, unlike GET /whoop/recovery which accepts and
// ignores it: the day-boundary contract is the API's to own, and a client must
// not learn that this endpoint works without one.
func TestGetSleepRequiresTimezone(t *testing.T) {
	d := newSleepDeps(t, "user-1")
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/sleep")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "timezone is required") {
		t.Errorf("body = %s, want the daterange message", rec.Body.String())
	}
}

func TestGetSleepRejectsUnknownTimezone(t *testing.T) {
	d := newSleepDeps(t, "user-1")
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/whoop/sleep?timezone=Not/AZone")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid timezone") {
		t.Errorf("body = %s, want the daterange message", rec.Body.String())
	}
}

func TestGetSleepRejectsBadDates(t *testing.T) {
	d := newSleepDeps(t, "user-1")
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	for _, tc := range []struct{ query, want string }{
		{"/whoop/sleep?timezone=UTC&since=not-a-date", "invalid since (expected YYYY-MM-DD)"},
		{"/whoop/sleep?timezone=UTC&until=2026-13-99", "invalid until (expected YYYY-MM-DD)"},
	} {
		rec := hDoGet(router, tc.query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body=%s", tc.query, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: body = %s, want %q", tc.query, rec.Body.String(), tc.want)
		}
	}
}
