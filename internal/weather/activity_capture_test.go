package weather

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// captureCfg is svcCfg plus the capture knob on — the shipped default posture.
func captureCfg() config.WeatherConfig {
	cfg := svcCfg()
	cfg.CaptureActivityWeather = true
	return cfg
}

// newTestCapturer wires an ActivityCapturer over the REAL SQLite repository
// and budget ledger on one migrated DB, faking only the Provider — the same
// style as newTestService, and the reason these tests can assert "no row" and
// "no reservation" as facts about storage rather than about a mock.
func newTestCapturer(t *testing.T, cfg config.WeatherConfig, p Provider) (*ActivityCapturer, *SQLiteActivityWeatherRepository, *BudgetLedger, *sql.DB) {
	t.Helper()
	database := dbtest.New(t)
	mustInsertUser(t, database, "u1")
	repo := NewSQLiteActivityWeatherRepository(database)
	ledger := NewBudgetLedger(database)
	c := NewActivityCapturer(cfg, repo, ledger, p, testLogger())

	clock := func() time.Time { return captureNow }
	ledger.now = clock
	c.now = clock
	return c, repo, ledger, database
}

// captureNow is the capturer's fixed wall clock: an hour after the activity
// start the fixtures use, so fetched_at and observed_at can never be confused.
var captureNow = time.Date(2026, 8, 9, 15, 4, 5, 0, time.UTC)

func mustGetRow(t *testing.T, repo *SQLiteActivityWeatherRepository, activityID string) *ActivityWeather {
	t.Helper()
	row, err := repo.Get(context.Background(), activityID)
	if err != nil {
		t.Fatalf("Get %q: %v", activityID, err)
	}
	return row
}

func requireNoRow(t *testing.T, repo *SQLiteActivityWeatherRepository, activityID string) {
	t.Helper()
	if row := mustGetRow(t, repo, activityID); row != nil {
		t.Fatalf("Get %q = %+v, want no row (a failed capture must stay indistinguishable from one never attempted)", activityID, row)
	}
}

func TestCaptureStoresProviderObservation(t *testing.T) {
	p := newFakeProvider()
	c, repo, ledger, database := newTestCapturer(t, captureCfg(), p)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	if err := c.Capture(context.Background(), "a1", testLat, testLon, defaultActivityStart); err != nil {
		t.Fatalf("Capture: %v", err)
	}

	row := mustGetRow(t, repo, "a1")
	if row == nil {
		t.Fatal("Capture stored no row")
	}
	if row.Status != ActivityStatusOK {
		t.Errorf("Status = %q, want %q", row.Status, ActivityStatusOK)
	}
	if row.Lat == nil || *row.Lat != testLat || row.Lon == nil || *row.Lon != testLon {
		t.Errorf("Lat/Lon = %v/%v, want the coordinate that was actually used", row.Lat, row.Lon)
	}
	if row.Observation == nil {
		t.Fatal("Observation = nil on an ok row")
	}
	if *row.Observation != p.historical {
		t.Errorf("Observation = %+v, want the provider's answer %+v", *row.Observation, p.historical)
	}
	// observed_at is the provider's hour, never the requested one: a reading
	// must never be attributed to a timestamp it does not describe.
	if !row.Observation.ObservedAt.Equal(p.historical.ObservedAt) {
		t.Errorf("ObservedAt = %v, want the provider's %v", row.Observation.ObservedAt, p.historical.ObservedAt)
	}
	if !row.FetchedAt.Equal(captureNow) {
		t.Errorf("FetchedAt = %v, want the capture clock %v", row.FetchedAt, captureNow)
	}
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday = %d, want exactly 1 reserved call", got)
	}
}

// TestCaptureRoundsStartTimeToNearestHour: historical data is published
// hourly, so the request must name the hour the reading actually describes.
func TestCaptureRoundsStartTimeToNearestHour(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		want  time.Time
	}{
		{"rounds down", time.Date(2026, 8, 9, 14, 7, 0, 0, time.UTC), time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)},
		{"rounds up", time.Date(2026, 8, 9, 14, 40, 0, 0, time.UTC), time.Date(2026, 8, 9, 15, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProvider()
			c, _, _, database := newTestCapturer(t, captureCfg(), p)
			seedOutdoorRun(t, database, "a1", tc.start)

			if err := c.Capture(context.Background(), "a1", testLat, testLon, tc.start); err != nil {
				t.Fatalf("Capture: %v", err)
			}
			if !p.historicalAt.Equal(tc.want) {
				t.Errorf("provider asked for %v, want %v", p.historicalAt, tc.want)
			}
		})
	}
}

// TestCaptureRoundsToUTCHour pins the UTC conversion as well as the rounding:
// a non-UTC start must not be rounded in its own zone first.
func TestCaptureRoundsToUTCHour(t *testing.T) {
	p := newFakeProvider()
	c, _, _, database := newTestCapturer(t, captureCfg(), p)
	// India is +05:30, so a local hour boundary is a UTC half-hour — rounding
	// before converting would land 30 minutes off.
	zone := time.FixedZone("IST", 5*3600+1800)
	start := time.Date(2026, 8, 9, 14, 0, 0, 0, zone)
	seedOutdoorRun(t, database, "a1", start.UTC())

	if err := c.Capture(context.Background(), "a1", testLat, testLon, start); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	want := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC) // 08:30Z rounds to 09:00Z
	if !p.historicalAt.Equal(want) {
		t.Errorf("provider asked for %v, want %v", p.historicalAt, want)
	}
}

// TestCaptureDisabledMakesNoReservationAndNoCall: the disabled check runs
// first, so switching capture off during a backfill leaves the day's budget
// untouched rather than silently drawing it down.
func TestCaptureDisabledMakesNoReservationAndNoCall(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(cfg *config.WeatherConfig)
		unready func(p *fakeProvider)
	}{
		{"master switch off", func(cfg *config.WeatherConfig) { cfg.Enabled = false }, nil},
		{"capture knob off", func(cfg *config.WeatherConfig) { cfg.CaptureActivityWeather = false }, nil},
		{"provider unconfigured", nil, func(p *fakeProvider) { p.configured = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := captureCfg()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			p := newFakeProvider()
			if tc.unready != nil {
				tc.unready(p)
			}
			c, repo, ledger, database := newTestCapturer(t, cfg, p)
			seedOutdoorRun(t, database, "a1", defaultActivityStart)

			if c.Enabled() {
				t.Fatal("Enabled() = true, want false")
			}
			err := c.Capture(context.Background(), "a1", testLat, testLon, defaultActivityStart)
			if !errors.Is(err, ErrCaptureDisabled) {
				t.Fatalf("Capture = %v, want ErrCaptureDisabled", err)
			}
			if p.total() != 0 {
				t.Errorf("provider calls = %d, want 0", p.total())
			}
			if got := mustUsedToday(t, ledger); got != 0 {
				t.Errorf("UsedToday = %d, want 0", got)
			}
			requireNoRow(t, repo, "a1")
		})
	}
}

// TestCaptureBudgetExhaustedNeverCallsProvider is the ordering assertion the
// SOW cares about most: the reservation happens BEFORE the call, so a ceiling
// of zero cannot be spent past.
func TestCaptureBudgetExhaustedNeverCallsProvider(t *testing.T) {
	cfg := captureCfg()
	cfg.DailyCallBudget = 0
	p := newFakeProvider()
	c, repo, _, database := newTestCapturer(t, cfg, p)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	err := c.Capture(context.Background(), "a1", testLat, testLon, defaultActivityStart)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Capture = %v, want ErrBudgetExhausted", err)
	}
	if p.total() != 0 {
		t.Errorf("provider calls = %d, want 0 — the ledger must gate the call", p.total())
	}
	requireNoRow(t, repo, "a1")
}

// TestCaptureTransientErrorWritesNoRow: the activity stays eligible, so the
// backfill is the single repair path.
func TestCaptureTransientErrorWritesNoRow(t *testing.T) {
	p := newFakeProvider()
	boom := errors.New("openweather: 503")
	p.errs[EndpointHistorical] = boom
	c, repo, ledger, database := newTestCapturer(t, captureCfg(), p)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	if err := c.Capture(context.Background(), "a1", testLat, testLon, defaultActivityStart); !errors.Is(err, boom) {
		t.Fatalf("Capture = %v, want the provider error", err)
	}
	requireNoRow(t, repo, "a1")
	// The call was reserved before it failed: a reservation is never refunded,
	// which is the safe direction for a spend cap.
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday = %d, want 1", got)
	}
}

// captureLabels is every band of activity_captures_total, so a test can assert
// that an outcome moved NONE of them rather than only the one it thought of.
var captureLabels = []string{
	"ok", "failed",
	string(ActivityStatusNoCoordinates), string(ActivityStatusUnavailable),
}

func readCaptureCounters(t *testing.T) map[string]float64 {
	t.Helper()
	out := make(map[string]float64, len(captureLabels))
	for _, label := range captureLabels {
		out[label] = testutil.ToFloat64(activityCapturesTotal.WithLabelValues(label))
	}
	return out
}

// TestCaptureNoHistoricalDataWritesNoRow: the definitive negative is returned
// unwritten. Only the backfill turns it into a terminal 'unavailable' row, so
// an import-time miss stays retryable.
func TestCaptureNoHistoricalDataWritesNoRow(t *testing.T) {
	p := newFakeProvider()
	p.errs[EndpointHistorical] = ErrNoHistoricalData
	c, repo, _, database := newTestCapturer(t, captureCfg(), p)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	before := readCaptureCounters(t)
	if err := c.Capture(context.Background(), "a1", testLat, testLon, defaultActivityStart); !errors.Is(err, ErrNoHistoricalData) {
		t.Fatalf("Capture = %v, want ErrNoHistoricalData", err)
	}
	requireNoRow(t, repo, "a1")

	// No band moves, `unavailable` least of all: this capturer wrote no row,
	// and the row the backfill may later write is what earns that count. The
	// spend is already visible in provider_calls_total{historical,error}, so a
	// `failed` increment here would double-count it into the band the
	// dashboard reads as "stop and look".
	for label, want := range before {
		if got := testutil.ToFloat64(activityCapturesTotal.WithLabelValues(label)); got != want {
			t.Errorf("activity_captures_total{%s} moved by %v, want 0", label, got-want)
		}
	}
}

// TestCaptureFeedsSharedProviderMetrics pins the observeCall refactor:
// historical calls land in the very series the shipped dashboard's "Calls by
// endpoint" panel already draws, with no new metric.
func TestCaptureFeedsSharedProviderMetrics(t *testing.T) {
	p := newFakeProvider()
	c, _, _, database := newTestCapturer(t, captureCfg(), p)
	seedOutdoorRun(t, database, "ok-row", defaultActivityStart)
	seedOutdoorRun(t, database, "err-row", defaultActivityStart)
	ctx := context.Background()

	okDelta := counterDelta(t,
		func() float64 {
			return testutil.ToFloat64(providerCallsTotal.WithLabelValues(string(EndpointHistorical), "ok"))
		},
		func() {
			if err := c.Capture(ctx, "ok-row", testLat, testLon, defaultActivityStart); err != nil {
				t.Fatalf("Capture: %v", err)
			}
		})
	if okDelta != 1 {
		t.Errorf("provider_calls_total{historical,ok} delta = %v, want 1", okDelta)
	}

	p.errs[EndpointHistorical] = errors.New("openweather: 503")
	errDelta := counterDelta(t,
		func() float64 {
			return testutil.ToFloat64(providerCallsTotal.WithLabelValues(string(EndpointHistorical), "error"))
		},
		func() {
			if err := c.Capture(ctx, "err-row", testLat, testLon, defaultActivityStart); err == nil {
				t.Fatal("Capture with a failing provider = nil error")
			}
		})
	if errDelta != 1 {
		t.Errorf("provider_calls_total{historical,error} delta = %v, want 1", errDelta)
	}
	if got := testutil.CollectAndCount(providerLatency); got == 0 {
		t.Error("provider latency histogram recorded nothing")
	}
}

func TestCaptureCountsResults(t *testing.T) {
	p := newFakeProvider()
	c, _, _, database := newTestCapturer(t, captureCfg(), p)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)
	ctx := context.Background()

	delta := counterDelta(t,
		func() float64 { return testutil.ToFloat64(activityCapturesTotal.WithLabelValues("ok")) },
		func() {
			if err := c.Capture(ctx, "a1", testLat, testLon, defaultActivityStart); err != nil {
				t.Fatalf("Capture: %v", err)
			}
		})
	if delta != 1 {
		t.Errorf("activity_captures_total{ok} delta = %v, want 1", delta)
	}

	// A budget refusal is deliberately NOT a `failed` capture: a backfill
	// stopping on the day's ceiling is a clean stop, and the dashboard reads a
	// rising `failed` band as "stop and look".
	c.cfg.DailyCallBudget = 0
	failedDelta := counterDelta(t,
		func() float64 { return testutil.ToFloat64(activityCapturesTotal.WithLabelValues("failed")) },
		func() {
			if err := c.Capture(ctx, "a1", testLat, testLon, defaultActivityStart); !errors.Is(err, ErrBudgetExhausted) {
				t.Fatalf("Capture = %v, want ErrBudgetExhausted", err)
			}
		})
	if failedDelta != 0 {
		t.Errorf("activity_captures_total{failed} delta on budget exhaustion = %v, want 0", failedDelta)
	}
}

// TestRecordNoCoordinatesWritesTerminalRowWithoutCalling: no provider call, no
// reservation — position cannot be re-derived, so there is nothing to ask.
func TestRecordNoCoordinatesWritesTerminalRowWithoutCalling(t *testing.T) {
	p := newFakeProvider()
	c, repo, ledger, database := newTestCapturer(t, captureCfg(), p)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	delta := counterDelta(t,
		func() float64 { return testutil.ToFloat64(activityCapturesTotal.WithLabelValues("no_coordinates")) },
		func() {
			if err := c.RecordNoCoordinates(context.Background(), "a1"); err != nil {
				t.Fatalf("RecordNoCoordinates: %v", err)
			}
		})
	if delta != 1 {
		t.Errorf("activity_captures_total{no_coordinates} delta = %v, want 1", delta)
	}

	row := mustGetRow(t, repo, "a1")
	if row == nil || row.Status != ActivityStatusNoCoordinates {
		t.Fatalf("row = %+v, want a no_coordinates row", row)
	}
	if row.Lat != nil || row.Lon != nil || row.Observation != nil {
		t.Errorf("row = %+v, want NULL coordinates and no reading", row)
	}
	if !row.FetchedAt.Equal(captureNow) {
		t.Errorf("FetchedAt = %v, want %v", row.FetchedAt, captureNow)
	}
	if p.total() != 0 {
		t.Errorf("provider calls = %d, want 0", p.total())
	}
	if got := mustUsedToday(t, ledger); got != 0 {
		t.Errorf("UsedToday = %d, want 0 — a free resolution must not spend budget", got)
	}
}

// TestRecordUnavailableWritesTerminalRowWithCoordinate: the coordinate is
// kept so --retry-unavailable can tell "we asked and were refused" from "there
// was nothing to ask about".
func TestRecordUnavailableWritesTerminalRowWithCoordinate(t *testing.T) {
	p := newFakeProvider()
	c, repo, ledger, database := newTestCapturer(t, captureCfg(), p)
	seedOutdoorRun(t, database, "a1", defaultActivityStart)

	delta := counterDelta(t,
		func() float64 { return testutil.ToFloat64(activityCapturesTotal.WithLabelValues("unavailable")) },
		func() {
			if err := c.RecordUnavailable(context.Background(), "a1", testLat, testLon); err != nil {
				t.Fatalf("RecordUnavailable: %v", err)
			}
		})
	if delta != 1 {
		t.Errorf("activity_captures_total{unavailable} delta = %v, want 1", delta)
	}

	row := mustGetRow(t, repo, "a1")
	if row == nil || row.Status != ActivityStatusUnavailable {
		t.Fatalf("row = %+v, want an unavailable row", row)
	}
	if row.Lat == nil || *row.Lat != testLat || row.Lon == nil || *row.Lon != testLon {
		t.Errorf("Lat/Lon = %v/%v, want the coordinate that was tried", row.Lat, row.Lon)
	}
	if row.Observation != nil {
		t.Errorf("Observation = %+v, want nil on a non-ok row", row.Observation)
	}
	if p.total() != 0 {
		t.Errorf("provider calls = %d, want 0", p.total())
	}
	if got := mustUsedToday(t, ledger); got != 0 {
		t.Errorf("UsedToday = %d, want 0 — the call that produced this was already reserved", got)
	}
}

// TestCapturerGetPassesThroughRepository: one seam for the activity handler.
func TestCapturerGetPassesThroughRepository(t *testing.T) {
	c, repo, _, database := newTestCapturer(t, captureCfg(), newFakeProvider())
	seedOutdoorRun(t, database, "a1", defaultActivityStart)
	ctx := context.Background()

	got, err := c.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("Get on an absent row: %v", err)
	}
	if got != nil {
		t.Fatalf("Get on an absent row = %+v, want nil", got)
	}

	if err = repo.Put(ctx, okRow("a1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err = c.Get(ctx, "a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Status != ActivityStatusOK {
		t.Fatalf("Get = %+v, want the stored ok row", got)
	}
}
