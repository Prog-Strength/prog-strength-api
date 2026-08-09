package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"log/slog"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	"github.com/Prog-Strength/prog-strength-api/internal/weather"
)

// The whole suite drives the real backfill loop over a real SQLite database
// and a real weather.ActivityCapturer — only the provider and the pacer are
// faked. That is what lets "zero provider calls" and "no row" be assertions
// about the vendor seam and about storage, rather than about a mock of the
// thing under test.

var (
	baseStart = time.Date(2026, 8, 9, 14, 7, 0, 0, time.UTC)
	testObs   = weather.Observation{
		ObservedAt: time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC),
		TempC:      31.8,
		FeelsLikeC: 33.4,
		DewPointC:  12.5,
		Humidity:   28,
		WindKMH:    18.36,
		WindDeg:    210,
		PrecipMM:   0,
		Condition:  "Clear",
		Icon:       "01d",
	}
)

// fakeProvider answers Historical from canned data and counts every call, so
// --dry-run's "zero provider calls" is a measured fact. Failures are keyed by
// latitude because Historical is handed a coordinate, not an activity id, and
// each fixture is seeded at its own latitude.
type fakeProvider struct {
	calls    int
	errByLat map[float64]error
}

func (p *fakeProvider) Configured() bool { return true }

func (p *fakeProvider) Historical(_ context.Context, lat, _ float64, _ time.Time) (weather.Observation, error) {
	p.calls++
	if err, ok := p.errByLat[lat]; ok {
		return weather.Observation{}, err
	}
	return testObs, nil
}

// The rest of the Provider seam is unreachable from the backfill; it exists
// only so the fake satisfies the interface.
func (p *fakeProvider) Current(context.Context, float64, float64) (weather.Current, error) {
	return weather.Current{}, errors.New("not used by the backfill")
}

func (p *fakeProvider) Hourly(context.Context, float64, float64) ([]weather.HourlyBucket, error) {
	return nil, errors.New("not used by the backfill")
}

func (p *fakeProvider) Daily(context.Context, float64, float64) (weather.Daily, error) {
	return weather.Daily{}, errors.New("not used by the backfill")
}

func (p *fakeProvider) GeocodeDirect(context.Context, string, int) ([]weather.GeoResult, error) {
	return nil, errors.New("not used by the backfill")
}

func (p *fakeProvider) GeocodeReverse(context.Context, float64, float64) ([]weather.GeoResult, error) {
	return nil, errors.New("not used by the backfill")
}

// fakePacer returns immediately and counts, so the suite exercises the real
// pacing call site without spending --rate seconds per activity. cancelAfter
// makes it stand in for a SIGINT arriving mid-run.
type fakePacer struct {
	waits       int
	cancelAfter int
	cancel      context.CancelFunc
}

func (p *fakePacer) wait(ctx context.Context) error {
	p.waits++
	if p.cancel != nil && p.waits > p.cancelAfter {
		p.cancel()
		return context.Canceled
	}
	return ctx.Err()
}

func (p *fakePacer) stop() {}

type env struct {
	t        *testing.T
	db       *sql.DB
	repo     *weather.SQLiteActivityWeatherRepository
	ledger   *weather.BudgetLedger
	provider *fakeProvider
	pacer    *fakePacer
	deps     deps
}

func newEnv(t *testing.T, budget int) *env {
	t.Helper()
	database := dbtest.New(t)
	mustExec(t, database, `
		INSERT INTO users (id, email, display_name, weight_unit, created_at, updated_at)
		VALUES ('u1', 'u1@example.com', 'u1', 'lb', '2026-01-01', '2026-01-01')
	`)

	repo := weather.NewSQLiteActivityWeatherRepository(database)
	ledger := weather.NewBudgetLedger(database)
	provider := &fakeProvider{errByLat: map[float64]error{}}
	pacer := &fakePacer{}
	cfg := config.WeatherConfig{
		Enabled:                true,
		APIKey:                 "test-key",
		DailyCallBudget:        budget,
		DailyCallHardCeiling:   budget,
		CaptureActivityWeather: true,
	}

	return &env{
		t:        t,
		db:       database,
		repo:     repo,
		ledger:   ledger,
		provider: provider,
		pacer:    pacer,
		deps: deps{
			repo:     repo,
			capturer: weather.NewActivityCapturer(cfg, repo, ledger, provider, slog.New(slog.NewTextHandler(io.Discard, nil))),
			pacer:    pacer,
			logger:   log.New(io.Discard, "", 0),
		},
	}
}

func (e *env) run(opts options) {
	e.t.Helper()
	if err := backfill(context.Background(), e.deps, opts); err != nil {
		e.t.Fatalf("backfill: %v", err)
	}
}

func mustExec(t *testing.T, database *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// seedActivity inserts a live activity plus the run-detail row that carries
// `environment` (it lives per activity type, not on activities — migration
// 042). Each activity starts an hour apart so newest-first ordering is
// unambiguous.
func (e *env) seedActivity(id string, minutesOld int, environment string) {
	e.t.Helper()
	start := baseStart.Add(-time.Duration(minutesOld) * time.Minute)
	mustExec(e.t, e.db, `
		INSERT INTO activities (id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, duration_seconds, tcx_s3_key, created_at, updated_at)
		VALUES (?, 'u1', 'running', 'manual_tcx', ?, ?, 1800, '', '2026-08-09T15:00:00Z', '2026-08-09T15:00:00Z')
	`, id, id, start)
	mustExec(e.t, e.db, `
		INSERT INTO activity_run_details (activity_id, distance_meters, raw_distance_meters, environment)
		VALUES (?, 10000, 10000, ?)
	`, id, environment)
}

// seedOutdoor seeds an eligible activity at its own latitude, which is the key
// the fake provider fails on.
func (e *env) seedOutdoor(id string, minutesOld int, lat float64) {
	e.t.Helper()
	e.seedActivity(id, minutesOld, "outdoor")
	mustExec(e.t, e.db, `
		INSERT INTO activity_trackpoints (activity_id, sequence, elapsed_seconds, distance_meters, latitude, longitude)
		VALUES (?, 0, 0, 0, ?, -104.9847)
	`, id, lat)
}

// seedOutdoorNoGPS seeds an outdoor activity whose only trackpoint carries no
// position — a dropped fix, and the free half of the --dry-run split.
func (e *env) seedOutdoorNoGPS(id string, minutesOld int) {
	e.t.Helper()
	e.seedActivity(id, minutesOld, "outdoor")
	mustExec(e.t, e.db, `
		INSERT INTO activity_trackpoints (activity_id, sequence, elapsed_seconds, distance_meters, latitude, longitude)
		VALUES (?, 0, 0, 0, NULL, NULL)
	`, id)
}

func (e *env) row(id string) *weather.ActivityWeather {
	e.t.Helper()
	row, err := e.repo.Get(context.Background(), id)
	if err != nil {
		e.t.Fatalf("Get %q: %v", id, err)
	}
	return row
}

func (e *env) requireStatus(id string, want weather.ActivityStatus) {
	e.t.Helper()
	row := e.row(id)
	if row == nil {
		e.t.Fatalf("activity %q has no row, want status %q", id, want)
	}
	if row.Status != want {
		e.t.Errorf("activity %q status = %q, want %q", id, row.Status, want)
	}
}

func (e *env) requireNoRow(id string) {
	e.t.Helper()
	if row := e.row(id); row != nil {
		e.t.Fatalf("activity %q = %+v, want no row (an unresolved activity must stay eligible)", id, row)
	}
}

func (e *env) requireCalls(want int) {
	e.t.Helper()
	if e.provider.calls != want {
		e.t.Errorf("provider calls = %d, want %d", e.provider.calls, want)
	}
}

func (e *env) usedBudget() int {
	e.t.Helper()
	used, err := e.ledger.UsedToday(context.Background())
	if err != nil {
		e.t.Fatalf("UsedToday: %v", err)
	}
	return used
}

func TestDryRunSpendsNothing(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoor("a1", 0, 10.1)
	e.seedOutdoor("a2", 60, 10.2)
	e.seedOutdoorNoGPS("a3", 120)

	e.run(options{dryRun: true, rate: 1})

	// Three independent proofs that nothing happened: the vendor seam was
	// never touched, the ledger recorded no reservation, and the store is
	// still empty — including the free no_coordinates row, which --dry-run
	// must also leave unwritten.
	e.requireCalls(0)
	if used := e.usedBudget(); used != 0 {
		t.Errorf("budget used = %d, want 0 (a dry run must reserve nothing)", used)
	}
	if e.pacer.waits != 0 {
		t.Errorf("pacer waits = %d, want 0", e.pacer.waits)
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		e.requireNoRow(id)
	}
}

func TestLimitCapsProviderCalls(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoor("newest", 0, 10.1)
	e.seedOutdoor("middle", 60, 10.2)
	e.seedOutdoor("oldest", 120, 10.3)

	e.run(options{limit: 2, rate: 1})

	e.requireCalls(2)
	if e.pacer.waits != 2 {
		t.Errorf("pacer waits = %d, want one per provider call (2)", e.pacer.waits)
	}
	// Newest-first: the two most recent activities are the ones the budget
	// bought, and the remainder is untouched rather than half-written.
	e.requireStatus("newest", weather.ActivityStatusOK)
	e.requireStatus("middle", weather.ActivityStatusOK)
	e.requireNoRow("oldest")
}

func TestNoGPSActivitiesAreFreeAndDoNotCountAgainstLimit(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoorNoGPS("nogps1", 0)
	e.seedOutdoorNoGPS("nogps2", 60)
	e.seedOutdoor("gps", 120, 10.1)

	e.run(options{limit: 1, rate: 1})

	// One call for the one eligible activity: the two GPS-less ones were
	// resolved without spending, so they must not have consumed the limit.
	e.requireCalls(1)
	if used := e.usedBudget(); used != 1 {
		t.Errorf("budget used = %d, want 1 (no_coordinates rows reserve nothing)", used)
	}
	e.requireStatus("nogps1", weather.ActivityStatusNoCoordinates)
	e.requireStatus("nogps2", weather.ActivityStatusNoCoordinates)
	e.requireStatus("gps", weather.ActivityStatusOK)
}

func TestBudgetExhaustionStopsCleanlyMidRun(t *testing.T) {
	e := newEnv(t, 2)
	e.seedOutdoor("a1", 0, 10.1)
	e.seedOutdoor("a2", 60, 10.2)
	e.seedOutdoor("a3", 120, 10.3)

	// No error: a budget stop is the expected end of a backfill day, so the
	// process exits 0 and the operator's shell chain keeps working.
	e.run(options{rate: 1})

	e.requireCalls(2)
	e.requireStatus("a1", weather.ActivityStatusOK)
	e.requireStatus("a2", weather.ActivityStatusOK)
	e.requireNoRow("a3")
}

func TestRerunResumesWithoutRespending(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoor("a1", 0, 10.1)
	e.seedOutdoor("a2", 60, 10.2)

	e.run(options{limit: 1, rate: 1})
	e.requireCalls(1)

	e.run(options{rate: 1})

	// Two calls total across both runs, not three: the presence of a row is
	// the checkpoint, so the resolved activity is never re-fetched.
	e.requireCalls(2)
	e.requireStatus("a1", weather.ActivityStatusOK)
	e.requireStatus("a2", weather.ActivityStatusOK)
}

func TestTerminalRowsAreNeverReattempted(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoorNoGPS("nogps", 0)
	e.seedOutdoor("gone", 60, 10.2)
	e.provider.errByLat[10.2] = weather.ErrNoHistoricalData

	e.run(options{rate: 1})
	e.requireCalls(1)
	e.requireStatus("nogps", weather.ActivityStatusNoCoordinates)
	e.requireStatus("gone", weather.ActivityStatusUnavailable)

	e.run(options{rate: 1})

	// Still one call: both statuses are terminal, so a second run finds
	// nothing pending and spends nothing.
	e.requireCalls(1)
}

func TestRetryUnavailableClearsOnlyUnavailable(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoorNoGPS("nogps", 0)
	e.seedOutdoor("gone", 60, 10.2)
	e.provider.errByLat[10.2] = weather.ErrNoHistoricalData

	e.run(options{rate: 1})
	e.requireStatus("gone", weather.ActivityStatusUnavailable)

	// The provider's coverage changed: the second attempt succeeds.
	delete(e.provider.errByLat, 10.2)
	e.run(options{retryUnavailable: true, rate: 1})

	e.requireStatus("gone", weather.ActivityStatusOK)
	// no_coordinates survives: a position that was never recorded cannot be
	// re-derived, so retrying it would spend a call to learn nothing.
	e.requireStatus("nogps", weather.ActivityStatusNoCoordinates)
}

func TestIndoorActivityIsSkippedUntilRetagged(t *testing.T) {
	e := newEnv(t, 100)
	e.seedActivity("treadmill", 0, "indoor")
	mustExec(t, e.db, `
		INSERT INTO activity_trackpoints (activity_id, sequence, elapsed_seconds, distance_meters, latitude, longitude)
		VALUES ('treadmill', 0, 0, 0, 10.1, -104.9847)
	`)

	e.run(options{rate: 1})
	e.requireCalls(0)
	e.requireNoRow("treadmill")

	mustExec(t, e.db, `UPDATE activity_run_details SET environment = 'outdoor' WHERE activity_id = 'treadmill'`)
	e.run(options{rate: 1})

	// Retagging makes new work appear long after the "one-time" migration is
	// done, which is why the command is re-runnable rather than one-shot.
	e.requireCalls(1)
	e.requireStatus("treadmill", weather.ActivityStatusOK)
}

func TestTransientErrorLeavesNoRowAndTheRunContinues(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoor("flaky", 0, 10.1)
	e.seedOutdoor("fine", 60, 10.2)
	e.provider.errByLat[10.1] = errors.New("openweather: 503 service unavailable")

	e.run(options{rate: 1})

	e.requireCalls(2)
	// No row, so the activity is simply still eligible and the next run
	// retries it — one bad activity must not end the run.
	e.requireNoRow("flaky")
	e.requireStatus("fine", weather.ActivityStatusOK)
}

func TestInterruptStopsBetweenActivities(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoor("a1", 0, 10.1)
	e.seedOutdoor("a2", 60, 10.2)
	e.seedOutdoor("a3", 120, 10.3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.pacer.cancel, e.pacer.cancelAfter = cancel, 1

	if err := backfill(ctx, e.deps, options{rate: 1}); err != nil {
		t.Fatalf("backfill after interrupt: %v", err)
	}

	// The first activity's row committed; the signal stopped the loop before
	// the second call, leaving a consistent database to resume from.
	e.requireCalls(1)
	e.requireStatus("a1", weather.ActivityStatusOK)
	e.requireNoRow("a2")
	e.requireNoRow("a3")
}

// cancellingCapturer fires a signal-like cancel once one activity has been
// resolved for free, so the loop's between-activities check is exercised on
// the no-coordinates path — where the pacer is never consulted and would
// therefore never notice the interrupt.
type cancellingCapturer struct {
	capturer
	cancel context.CancelFunc
}

func (c *cancellingCapturer) RecordNoCoordinates(ctx context.Context, activityID string) error {
	err := c.capturer.RecordNoCoordinates(ctx, activityID)
	c.cancel()
	return err
}

func TestInterruptStopsBetweenFreeActivities(t *testing.T) {
	e := newEnv(t, 100)
	e.seedOutdoorNoGPS("a1", 0)
	e.seedOutdoorNoGPS("a2", 60)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.deps.capturer = &cancellingCapturer{capturer: e.deps.capturer, cancel: cancel}

	if err := backfill(ctx, e.deps, options{rate: 1}); err != nil {
		t.Fatalf("backfill after interrupt: %v", err)
	}

	e.requireStatus("a1", weather.ActivityStatusNoCoordinates)
	e.requireNoRow("a2")
}

func TestPaceIntervalConvertsCallsPerSecond(t *testing.T) {
	cases := []struct {
		rate float64
		want time.Duration
	}{
		{1, time.Second},
		{2, 500 * time.Millisecond},
		{0.5, 2 * time.Second},
		// Absurdly fast still has to be a legal ticker interval:
		// time.NewTicker panics on a non-positive duration.
		{1e12, 1},
	}
	for _, tc := range cases {
		if got := paceInterval(tc.rate); got != tc.want {
			t.Errorf("paceInterval(%v) = %v, want %v", tc.rate, got, tc.want)
		}
	}
}

func TestTickerPacerWaitReturnsOnCancelledContext(t *testing.T) {
	// A one-hour interval so only the cancelled context can win the select —
	// the assertion is that a Ctrl-C does not have to wait out the pace.
	p := &tickerPacer{t: time.NewTicker(time.Hour)}
	defer p.stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait on a cancelled context = %v, want context.Canceled", err)
	}
}
