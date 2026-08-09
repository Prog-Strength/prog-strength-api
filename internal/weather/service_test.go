package weather

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// Test coordinates. ReadingKey rounds to 2 decimals, so seeds and reads must
// share these exact values to land on the same cache rows.
const (
	testLat = 39.7392
	testLon = -104.9903
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// svcCfg returns the SOW's TTLs (15/60/180 min, 30-day geocoding) with a
// budget high enough to never interfere unless a test lowers it.
func svcCfg() config.WeatherConfig {
	return config.WeatherConfig{
		Enabled:              true,
		APIKey:               "test-key",
		DailyCallBudget:      100,
		AllowPaidOverage:     false,
		DailyCallHardCeiling: 2000,
		CountGeocodingCalls:  true,
		CurrentTTLMinutes:    15,
		HourlyTTLMinutes:     60,
		DailyTTLMinutes:      180,
		GeocodingTTLDays:     30,
	}
}

// fakeProvider hands back canned per-endpoint values, injects per-endpoint
// errors, and counts calls — the only fake in these tests; cache and ledger
// are the real SQLite implementations over dbtest.
type fakeProvider struct {
	configured bool
	current    Current
	hourly     []HourlyBucket
	daily      Daily
	direct     []GeoResult
	reverse    []GeoResult
	historical Observation
	errs       map[Endpoint]error
	calls      map[Endpoint]int
}

func newFakeProvider() *fakeProvider {
	sunrise := time.Date(2026, 8, 9, 6, 2, 0, 0, time.UTC)
	return &fakeProvider{
		configured: true,
		current:    Current{TempC: 21.5, FeelsLikeC: 20.1, Humidity: 40, WindKMH: 10.8, Condition: "Clear", Icon: "01d"},
		hourly:     []HourlyBucket{{At: time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC), TempC: 23.0, Icon: "01d"}},
		daily:      Daily{HighC: 28.5, LowC: 14.0, Sunrise: sunrise, Sunset: sunrise.Add(14 * time.Hour)},
		direct:     []GeoResult{{Name: "Denver", State: "Colorado", Country: "US", Lat: testLat, Lon: testLon}},
		reverse:    []GeoResult{{Name: "Denver", State: "Colorado", Country: "US", Lat: testLat, Lon: testLon}},
		historical: Observation{
			ObservedAt: time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC),
			TempC:      18.2, FeelsLikeC: 17.4, DewPointC: 6.1, Humidity: 44,
			WindKMH: 12.6, WindDeg: 210, PrecipMM: 0, Condition: "Clear", Icon: "01d",
		},
		errs:  map[Endpoint]error{},
		calls: map[Endpoint]int{},
	}
}

func (f *fakeProvider) Configured() bool { return f.configured }

func (f *fakeProvider) Current(ctx context.Context, lat, lon float64) (Current, error) {
	f.calls[EndpointCurrent]++
	if err := f.errs[EndpointCurrent]; err != nil {
		return Current{}, err
	}
	return f.current, nil
}

func (f *fakeProvider) Hourly(ctx context.Context, lat, lon float64) ([]HourlyBucket, error) {
	f.calls[EndpointHourly]++
	if err := f.errs[EndpointHourly]; err != nil {
		return nil, err
	}
	return f.hourly, nil
}

func (f *fakeProvider) Daily(ctx context.Context, lat, lon float64) (Daily, error) {
	f.calls[EndpointDaily]++
	if err := f.errs[EndpointDaily]; err != nil {
		return Daily{}, err
	}
	return f.daily, nil
}

func (f *fakeProvider) GeocodeDirect(ctx context.Context, query string, limit int) ([]GeoResult, error) {
	f.calls[EndpointGeocodeDirect]++
	if err := f.errs[EndpointGeocodeDirect]; err != nil {
		return nil, err
	}
	return f.direct, nil
}

func (f *fakeProvider) GeocodeReverse(ctx context.Context, lat, lon float64) ([]GeoResult, error) {
	f.calls[EndpointGeocodeReverse]++
	if err := f.errs[EndpointGeocodeReverse]; err != nil {
		return nil, err
	}
	return f.reverse, nil
}

func (f *fakeProvider) Historical(ctx context.Context, lat, lon float64, at time.Time) (Observation, error) {
	f.calls[EndpointHistorical]++
	if err := f.errs[EndpointHistorical]; err != nil {
		return Observation{}, err
	}
	return f.historical, nil
}

func (f *fakeProvider) total() int {
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// newTestService wires a Service over the REAL SQLite cache repo and budget
// ledger (one shared db) with a single settable clock shared by all three,
// mirroring nutritionlookup's test style: fake only the Provider.
func newTestService(t *testing.T, cfg config.WeatherConfig, p Provider) (*Service, *SQLiteCacheRepository, *BudgetLedger, *time.Time) {
	t.Helper()
	db := dbtest.New(t)
	cache := NewSQLiteCacheRepository(db)
	ledger := NewBudgetLedger(db)
	svc := NewService(cfg, cache, ledger, p, testLogger())

	current := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return current }
	cache.now = clock
	ledger.now = clock
	svc.now = clock
	return svc, cache, ledger, &current
}

// seedReading marshals payload and writes it as a cache row fetched at
// fetchedAt, so tests control per-endpoint age precisely.
func seedReading(t *testing.T, cache *SQLiteCacheRepository, key string, payload any, fetchedAt time.Time) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal seed payload for %q: %v", key, err)
	}
	if err := cache.Put(context.Background(), CacheRow{
		CacheKey:    key,
		PayloadJSON: string(b),
		FetchedAt:   fetchedAt,
		LastUsedAt:  fetchedAt,
	}); err != nil {
		t.Fatalf("seed cache row %q: %v", key, err)
	}
}

func mustUsedToday(t *testing.T, ledger *BudgetLedger) int {
	t.Helper()
	used, err := ledger.UsedToday(context.Background())
	if err != nil {
		t.Fatalf("UsedToday: %v", err)
	}
	return used
}

// counterDelta reads a counter, runs action, and returns the difference —
// counters live in the default registry and accumulate across tests.
func counterDelta(t *testing.T, read func() float64, action func()) float64 {
	t.Helper()
	before := read()
	action()
	return read() - before
}

// TestReadingsAllFreshServesFromCache: three fresh rows ⇒ zero provider
// calls, zero reservations, StatusOK, and FetchedAt is the OLDEST of the
// three — the honest "updated Xh ago".
func TestReadingsAllFreshServesFromCache(t *testing.T) {
	p := newFakeProvider()
	svc, cache, ledger, now := newTestService(t, svcCfg(), p)
	t0 := *now

	seeded := Current{TempC: 11.1, Condition: "Cached", Icon: "04d"}
	oldest := t0.Add(-40 * time.Minute)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointCurrent), seeded, t0.Add(-5*time.Minute))
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointHourly), p.hourly, t0.Add(-20*time.Minute))
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, oldest)

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
	}
	if p.total() != 0 {
		t.Errorf("provider calls = %d, want 0", p.total())
	}
	if got := mustUsedToday(t, ledger); got != 0 {
		t.Errorf("UsedToday = %d, want 0", got)
	}
	if !r.FetchedAt.Equal(oldest) {
		t.Errorf("FetchedAt = %v, want oldest row %v", r.FetchedAt, oldest)
	}
	if r.Current == nil || r.Current.TempC != seeded.TempC {
		t.Errorf("Current = %+v, want cached reading TempC=%v", r.Current, seeded.TempC)
	}
	if len(r.Hourly) != 1 || r.Daily == nil {
		t.Errorf("Hourly len=%d Daily=%v, want 1 bucket and a daily summary", len(r.Hourly), r.Daily)
	}
}

// TestReadingsColdCacheFetchesAllThree: empty cache ⇒ three provider calls,
// three reservations, three cache writes (proved by a second call hitting
// the cache), StatusOK, FetchedAt = now.
func TestReadingsColdCacheFetchesAllThree(t *testing.T) {
	p := newFakeProvider()
	svc, _, ledger, now := newTestService(t, svcCfg(), p)
	t0 := *now

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
	}
	for _, ep := range []Endpoint{EndpointCurrent, EndpointHourly, EndpointDaily} {
		if p.calls[ep] != 1 {
			t.Errorf("calls[%s] = %d, want 1", ep, p.calls[ep])
		}
	}
	if got := mustUsedToday(t, ledger); got != 3 {
		t.Errorf("UsedToday = %d, want 3", got)
	}
	if !r.FetchedAt.Equal(t0) {
		t.Errorf("FetchedAt = %v, want fetch time %v", r.FetchedAt, t0)
	}
	if r.Current == nil || r.Current.TempC != p.current.TempC {
		t.Errorf("Current = %+v, want provider reading TempC=%v", r.Current, p.current.TempC)
	}

	// The three writes must have landed: a second call is a pure cache hit.
	r2 := svc.Readings(context.Background(), testLat, testLon)
	if r2.Status != StatusOK {
		t.Fatalf("second Readings Status = %q, want %q", r2.Status, StatusOK)
	}
	if p.total() != 3 {
		t.Errorf("provider calls after warm re-read = %d, want still 3", p.total())
	}
	if got := mustUsedToday(t, ledger); got != 3 {
		t.Errorf("UsedToday after warm re-read = %d, want still 3", got)
	}
}

// TestReadingsPartialExpiryFetchesOnlyStaleEndpoint: current 16 min old
// (TTL 15) with hourly+daily fresh ⇒ exactly one provider call and a
// one-call reservation.
func TestReadingsPartialExpiryFetchesOnlyStaleEndpoint(t *testing.T) {
	p := newFakeProvider()
	svc, cache, ledger, now := newTestService(t, svcCfg(), p)
	t0 := *now

	stale := Current{TempC: 5.5, Condition: "Old", Icon: "04d"}
	freshAt := t0.Add(-5 * time.Minute)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointCurrent), stale, t0.Add(-16*time.Minute))
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointHourly), p.hourly, freshAt)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, freshAt)

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
	}
	if p.calls[EndpointCurrent] != 1 || p.total() != 1 {
		t.Errorf("calls = %v, want exactly one current call", p.calls)
	}
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday = %d, want 1", got)
	}
	if r.Current == nil || r.Current.TempC != p.current.TempC {
		t.Errorf("Current = %+v, want refetched reading TempC=%v", r.Current, p.current.TempC)
	}
	// Served set: current fetched now, hourly+daily from cache ⇒ oldest is
	// the cached rows' fetched_at.
	if !r.FetchedAt.Equal(freshAt) {
		t.Errorf("FetchedAt = %v, want %v", r.FetchedAt, freshAt)
	}
}

// TestReadingsTTLBoundaries pins the per-endpoint TTLs (15/60/180 min):
// age == TTL refetches, age just under does not.
func TestReadingsTTLBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		endpoint  Endpoint
		age       time.Duration
		wantCalls int
	}{
		{"current at 15m refetches", EndpointCurrent, 15 * time.Minute, 1},
		{"current at 14m serves cache", EndpointCurrent, 14 * time.Minute, 0},
		{"hourly at 60m refetches", EndpointHourly, 60 * time.Minute, 1},
		{"hourly at 59m serves cache", EndpointHourly, 59 * time.Minute, 0},
		{"daily at 180m refetches", EndpointDaily, 180 * time.Minute, 1},
		{"daily at 179m serves cache", EndpointDaily, 179 * time.Minute, 0},
	}
	payloads := func(p *fakeProvider) map[Endpoint]any {
		return map[Endpoint]any{
			EndpointCurrent: p.current,
			EndpointHourly:  p.hourly,
			EndpointDaily:   p.daily,
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProvider()
			svc, cache, _, now := newTestService(t, svcCfg(), p)
			t0 := *now
			for ep, payload := range payloads(p) {
				at := t0.Add(-time.Minute) // fresh for every endpoint
				if ep == tc.endpoint {
					at = t0.Add(-tc.age)
				}
				seedReading(t, cache, ReadingKey(testLat, testLon, ep), payload, at)
			}

			r := svc.Readings(context.Background(), testLat, testLon)
			if r.Status != StatusOK {
				t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
			}
			if p.calls[tc.endpoint] != tc.wantCalls {
				t.Errorf("calls[%s] = %d, want %d", tc.endpoint, p.calls[tc.endpoint], tc.wantCalls)
			}
			if p.total() != tc.wantCalls {
				t.Errorf("total calls = %d, want %d (only the boundary endpoint may fetch)", p.total(), tc.wantCalls)
			}
		})
	}
}

// TestReadingsStaleServeOnProviderError: expired current + erroring provider
// ⇒ the stale row is served, StatusStale, FetchedAt = the stale row's
// fetched_at.
func TestReadingsStaleServeOnProviderError(t *testing.T) {
	p := newFakeProvider()
	p.errs[EndpointCurrent] = errors.New("openweather 500")
	svc, cache, ledger, now := newTestService(t, svcCfg(), p)
	t0 := *now

	stale := Current{TempC: 5.5, Condition: "Old", Icon: "04d"}
	staleAt := t0.Add(-20 * time.Minute)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointCurrent), stale, staleAt)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointHourly), p.hourly, t0.Add(-5*time.Minute))
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, t0.Add(-5*time.Minute))

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusStale {
		t.Fatalf("Status = %q, want %q", r.Status, StatusStale)
	}
	if r.Current == nil || r.Current.TempC != stale.TempC {
		t.Errorf("Current = %+v, want stale reading TempC=%v", r.Current, stale.TempC)
	}
	if !r.FetchedAt.Equal(staleAt) {
		t.Errorf("FetchedAt = %v, want stale row's %v", r.FetchedAt, staleAt)
	}
	if p.calls[EndpointCurrent] != 1 {
		t.Errorf("current calls = %d, want 1 (the failed attempt)", p.calls[EndpointCurrent])
	}
	// Reserve-before-call: the failed attempt still consumed budget — the
	// safe direction for a spend cap.
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday = %d, want 1", got)
	}
}

// TestReadingsMissingForecastSectionDegradesToStale: a fresh current with a
// forecast section that was attempted and ended with nothing (fetch failed,
// no cache row) must NOT report ok/served with a silently thin payload — the
// missing section is degradation, so StatusStale with outcome served_stale.
func TestReadingsMissingForecastSectionDegradesToStale(t *testing.T) {
	p := newFakeProvider()
	p.errs[EndpointHourly] = errors.New("openweather 500")
	svc, cache, _, now := newTestService(t, svcCfg(), p)
	t0 := *now
	fresh := t0.Add(-time.Minute)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointCurrent), p.current, fresh)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, fresh)
	// No hourly row at all: the failed fetch has no stale fallback.

	var r Reading
	d := counterDelta(t, func() float64 {
		return testutil.ToFloat64(requestsTotal.WithLabelValues("served_stale"))
	}, func() {
		r = svc.Readings(context.Background(), testLat, testLon)
	})
	if r.Status != StatusStale {
		t.Fatalf("Status = %q, want %q (missing hourly section must degrade)", r.Status, StatusStale)
	}
	if d != 1 {
		t.Errorf("served_stale outcome delta = %v, want 1", d)
	}
	if r.Hourly != nil {
		t.Errorf("Hourly = %+v, want nil (section is missing)", r.Hourly)
	}
	if r.Current == nil || r.Current.TempC != p.current.TempC {
		t.Errorf("Current = %+v, want the fresh cached reading attached", r.Current)
	}
	if r.Daily == nil {
		t.Errorf("Daily = nil, want the fresh cached summary attached")
	}
	if p.calls[EndpointHourly] != 1 || p.total() != 1 {
		t.Errorf("calls = %v, want exactly one (failed) hourly attempt", p.calls)
	}
}

// TestReadingsCorruptCacheRowRefetches: an unparseable payload is treated as
// missing — counted as a corrupt cache event and refetched from the provider,
// not served and not an error.
func TestReadingsCorruptCacheRowRefetches(t *testing.T) {
	p := newFakeProvider()
	svc, cache, _, now := newTestService(t, svcCfg(), p)
	t0 := *now
	fresh := t0.Add(-time.Minute)
	if err := cache.Put(context.Background(), CacheRow{
		CacheKey:    ReadingKey(testLat, testLon, EndpointCurrent),
		PayloadJSON: "{corrupt", // would be fresh by age, but unparseable
		FetchedAt:   fresh,
		LastUsedAt:  fresh,
	}); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointHourly), p.hourly, fresh)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, fresh)

	var r Reading
	d := counterDelta(t, func() float64 {
		return testutil.ToFloat64(cacheEventsTotal.WithLabelValues("corrupt"))
	}, func() {
		r = svc.Readings(context.Background(), testLat, testLon)
	})
	if d != 1 {
		t.Errorf("corrupt cache event delta = %v, want 1", d)
	}
	if r.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (corrupt row refetched)", r.Status, StatusOK)
	}
	if p.calls[EndpointCurrent] != 1 || p.total() != 1 {
		t.Errorf("calls = %v, want exactly one current refetch", p.calls)
	}
	if r.Current == nil || r.Current.TempC != p.current.TempC {
		t.Errorf("Current = %+v, want the refetched reading TempC=%v", r.Current, p.current.TempC)
	}
}

// TestReadingsBudgetExhaustedServesStale: ledger at ceiling ⇒ zero provider
// calls, stale cache attached, StatusBudgetExhausted.
func TestReadingsBudgetExhaustedServesStale(t *testing.T) {
	cfg := svcCfg()
	cfg.DailyCallBudget = 2
	p := newFakeProvider()
	svc, cache, ledger, now := newTestService(t, cfg, p)
	t0 := *now

	if err := ledger.Reserve(context.Background(), 2, 2); err != nil {
		t.Fatalf("seed ledger to ceiling: %v", err)
	}
	stale := Current{TempC: 5.5, Condition: "Old", Icon: "04d"}
	staleAt := t0.Add(-200 * time.Minute) // stale for all three TTLs
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointCurrent), stale, staleAt)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointHourly), p.hourly, staleAt)
	seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, staleAt)

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusBudgetExhausted {
		t.Fatalf("Status = %q, want %q", r.Status, StatusBudgetExhausted)
	}
	if p.total() != 0 {
		t.Errorf("provider calls = %d, want 0", p.total())
	}
	if r.Current == nil || r.Current.TempC != stale.TempC {
		t.Errorf("Current = %+v, want stale reading TempC=%v", r.Current, stale.TempC)
	}
	if len(r.Hourly) == 0 || r.Daily == nil {
		t.Errorf("Hourly len=%d Daily=%v, want stale forecast attached", len(r.Hourly), r.Daily)
	}
	if !r.FetchedAt.Equal(staleAt) {
		t.Errorf("FetchedAt = %v, want %v", r.FetchedAt, staleAt)
	}
	if got := mustUsedToday(t, ledger); got != 2 {
		t.Errorf("UsedToday = %d, want unchanged 2", got)
	}
}

// TestReadingsHardFailureNoCache: cold cache + erroring provider ⇒
// StatusUnavailable with an empty payload.
func TestReadingsHardFailureNoCache(t *testing.T) {
	p := newFakeProvider()
	boom := errors.New("openweather down")
	p.errs[EndpointCurrent] = boom
	p.errs[EndpointHourly] = boom
	p.errs[EndpointDaily] = boom
	svc, _, _, _ := newTestService(t, svcCfg(), p)

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusUnavailable {
		t.Fatalf("Status = %q, want %q", r.Status, StatusUnavailable)
	}
	if r.Current != nil || len(r.Hourly) != 0 || r.Daily != nil {
		t.Errorf("payload = %+v, want empty", r)
	}
}

// TestReadingsDisabled: flag off ⇒ StatusDisabled, zero provider calls,
// zero reservations.
func TestReadingsDisabled(t *testing.T) {
	cfg := svcCfg()
	cfg.Enabled = false
	p := newFakeProvider()
	svc, _, ledger, _ := newTestService(t, cfg, p)

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusDisabled {
		t.Fatalf("Status = %q, want %q", r.Status, StatusDisabled)
	}
	if p.total() != 0 {
		t.Errorf("provider calls = %d, want 0", p.total())
	}
	if got := mustUsedToday(t, ledger); got != 0 {
		t.Errorf("UsedToday = %d, want 0", got)
	}
}

// TestReadingsUnconfiguredProviderBehavesAsDisabled: empty API key
// (Configured() false) is the same disposition as the flag being off.
func TestReadingsUnconfiguredProviderBehavesAsDisabled(t *testing.T) {
	p := newFakeProvider()
	p.configured = false
	svc, _, ledger, _ := newTestService(t, svcCfg(), p)

	r := svc.Readings(context.Background(), testLat, testLon)
	if r.Status != StatusDisabled {
		t.Fatalf("Status = %q, want %q", r.Status, StatusDisabled)
	}
	if p.total() != 0 {
		t.Errorf("provider calls = %d, want 0", p.total())
	}
	if got := mustUsedToday(t, ledger); got != 0 {
		t.Errorf("UsedToday = %d, want 0", got)
	}
}

// TestSearchColdThenWarm: a cold search reserves one call, hits the
// provider, and caches; the warm repeat serves the cache with no new
// reservation or call.
func TestSearchColdThenWarm(t *testing.T) {
	p := newFakeProvider()
	svc, _, ledger, _ := newTestService(t, svcCfg(), p)
	ctx := context.Background()

	results, status, err := svc.Search(ctx, "Denver  CO", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want %q", status, StatusOK)
	}
	if len(results) != 1 || results[0].Name != "Denver" {
		t.Errorf("results = %+v, want the canned Denver hit", results)
	}
	if p.calls[EndpointGeocodeDirect] != 1 {
		t.Errorf("geocode calls = %d, want 1", p.calls[EndpointGeocodeDirect])
	}
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday = %d, want 1", got)
	}

	// Warm: normalization makes "denver co" the same key; no reservation,
	// no provider call.
	results2, status2, err := svc.Search(ctx, "denver co", 5)
	if err != nil {
		t.Fatalf("warm Search: %v", err)
	}
	if status2 != StatusOK {
		t.Fatalf("warm status = %q, want %q", status2, StatusOK)
	}
	if len(results2) != 1 {
		t.Errorf("warm results = %+v, want the cached Denver hit", results2)
	}
	if p.calls[EndpointGeocodeDirect] != 1 {
		t.Errorf("geocode calls after warm search = %d, want still 1", p.calls[EndpointGeocodeDirect])
	}
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday after warm search = %d, want still 1", got)
	}
}

// TestSearchUncountedGeocodingSkipsReservationButStillCaches.
func TestSearchUncountedGeocodingSkipsReservationButStillCaches(t *testing.T) {
	cfg := svcCfg()
	cfg.CountGeocodingCalls = false
	p := newFakeProvider()
	svc, _, ledger, _ := newTestService(t, cfg, p)
	ctx := context.Background()

	_, status, err := svc.Search(ctx, "denver", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want %q", status, StatusOK)
	}
	if got := mustUsedToday(t, ledger); got != 0 {
		t.Errorf("UsedToday = %d, want 0 (geocoding uncounted)", got)
	}

	_, _, err = svc.Search(ctx, "denver", 5)
	if err != nil {
		t.Fatalf("warm Search: %v", err)
	}
	if p.calls[EndpointGeocodeDirect] != 1 {
		t.Errorf("geocode calls = %d, want 1 (second search served from cache)", p.calls[EndpointGeocodeDirect])
	}
}

// TestSearchCacheServeHonorsLimit: the cache key is limit-agnostic (rows are
// shared across callers), so a cached serve must still truncate to THIS
// caller's limit — a Search with limit 2 after a wider cached search returns
// exactly 2, without touching the provider again.
func TestSearchCacheServeHonorsLimit(t *testing.T) {
	p := newFakeProvider()
	p.direct = []GeoResult{
		{Name: "Springfield", State: "Illinois", Country: "US"},
		{Name: "Springfield", State: "Missouri", Country: "US"},
		{Name: "Springfield", State: "Massachusetts", Country: "US"},
		{Name: "Springfield", State: "Ohio", Country: "US"},
		{Name: "Springfield", State: "Oregon", Country: "US"},
	}
	svc, _, _, _ := newTestService(t, svcCfg(), p)
	ctx := context.Background()

	results, status, err := svc.Search(ctx, "springfield", 10)
	if err != nil {
		t.Fatalf("cold Search: %v", err)
	}
	if status != StatusOK || len(results) != 5 {
		t.Fatalf("cold Search = %d results status %q, want 5 results %q", len(results), status, StatusOK)
	}

	results2, status2, err := svc.Search(ctx, "springfield", 2)
	if err != nil {
		t.Fatalf("warm Search: %v", err)
	}
	if status2 != StatusOK {
		t.Fatalf("warm status = %q, want %q", status2, StatusOK)
	}
	if len(results2) != 2 {
		t.Errorf("warm results len = %d, want exactly 2 (cache serve must honor limit)", len(results2))
	}
	if p.calls[EndpointGeocodeDirect] != 1 {
		t.Errorf("geocode calls = %d, want 1 (limited re-search served from cache)", p.calls[EndpointGeocodeDirect])
	}
}

// TestSearchBudgetExhaustedServesExpiredRow: a geocode with an expired cache
// row whose reservation is refused serves the expired row as StatusStale with
// outcome served_stale — no provider call.
func TestSearchBudgetExhaustedServesExpiredRow(t *testing.T) {
	cfg := svcCfg()
	cfg.DailyCallBudget = 1
	p := newFakeProvider()
	svc, cache, ledger, now := newTestService(t, cfg, p)
	t0 := *now
	if err := ledger.Reserve(context.Background(), 1, 1); err != nil {
		t.Fatalf("seed ledger to ceiling: %v", err)
	}
	// 31 days old: past the 30-day geocoding TTL, inside the eviction window.
	seedReading(t, cache, GeocodeDirectKey("denver"), p.direct, t0.Add(-31*24*time.Hour))

	var (
		results []GeoResult
		status  Status
		err     error
	)
	d := counterDelta(t, func() float64 {
		return testutil.ToFloat64(requestsTotal.WithLabelValues("served_stale"))
	}, func() {
		results, status, err = svc.Search(context.Background(), "denver", 5)
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if status != StatusStale {
		t.Fatalf("status = %q, want %q", status, StatusStale)
	}
	if d != 1 {
		t.Errorf("served_stale outcome delta = %v, want 1", d)
	}
	if len(results) != 1 || results[0].Name != "Denver" {
		t.Errorf("results = %+v, want the expired Denver row", results)
	}
	if p.calls[EndpointGeocodeDirect] != 0 {
		t.Errorf("geocode calls = %d, want 0", p.calls[EndpointGeocodeDirect])
	}
}

// TestReverseColdThenWarm mirrors the direct-search flow on the reverse key.
func TestReverseColdThenWarm(t *testing.T) {
	p := newFakeProvider()
	svc, _, ledger, _ := newTestService(t, svcCfg(), p)
	ctx := context.Background()

	results, status, err := svc.Reverse(ctx, testLat, testLon)
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want %q", status, StatusOK)
	}
	if len(results) != 1 || results[0].Name != "Denver" {
		t.Errorf("results = %+v, want the canned Denver hit", results)
	}
	if p.calls[EndpointGeocodeReverse] != 1 {
		t.Errorf("reverse calls = %d, want 1", p.calls[EndpointGeocodeReverse])
	}
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday = %d, want 1", got)
	}

	_, status2, err := svc.Reverse(ctx, testLat, testLon)
	if err != nil {
		t.Fatalf("warm Reverse: %v", err)
	}
	if status2 != StatusOK {
		t.Fatalf("warm status = %q, want %q", status2, StatusOK)
	}
	if p.calls[EndpointGeocodeReverse] != 1 {
		t.Errorf("reverse calls after warm read = %d, want still 1", p.calls[EndpointGeocodeReverse])
	}
	if got := mustUsedToday(t, ledger); got != 1 {
		t.Errorf("UsedToday after warm read = %d, want still 1", got)
	}
}

// TestRequestsTotalOutcomes pins that a Readings call lands exactly one
// requestsTotal increment with its final outcome, across the four cheap
// dispositions to stage: cache_hit, budget_exhausted, served, served_stale.
func TestRequestsTotalOutcomes(t *testing.T) {
	outcome := func(label string) func() float64 {
		return func() float64 {
			return testutil.ToFloat64(requestsTotal.WithLabelValues(label))
		}
	}

	t.Run("cache_hit", func(t *testing.T) {
		p := newFakeProvider()
		svc, cache, _, now := newTestService(t, svcCfg(), p)
		t0 := *now
		fresh := t0.Add(-time.Minute)
		seedReading(t, cache, ReadingKey(testLat, testLon, EndpointCurrent), p.current, fresh)
		seedReading(t, cache, ReadingKey(testLat, testLon, EndpointHourly), p.hourly, fresh)
		seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, fresh)

		d := counterDelta(t, outcome("cache_hit"), func() {
			if r := svc.Readings(context.Background(), testLat, testLon); r.Status != StatusOK {
				t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
			}
		})
		if d != 1 {
			t.Errorf("cache_hit outcome delta = %v, want 1", d)
		}
	})

	t.Run("budget_exhausted", func(t *testing.T) {
		cfg := svcCfg()
		cfg.DailyCallBudget = 1
		p := newFakeProvider()
		svc, _, ledger, _ := newTestService(t, cfg, p)
		if err := ledger.Reserve(context.Background(), 1, 1); err != nil {
			t.Fatalf("seed ledger to ceiling: %v", err)
		}

		d := counterDelta(t, outcome("budget_exhausted"), func() {
			if r := svc.Readings(context.Background(), testLat, testLon); r.Status != StatusBudgetExhausted {
				t.Fatalf("Status = %q, want %q", r.Status, StatusBudgetExhausted)
			}
		})
		if d != 1 {
			t.Errorf("budget_exhausted outcome delta = %v, want 1", d)
		}
	})

	t.Run("served", func(t *testing.T) {
		p := newFakeProvider()
		svc, _, _, _ := newTestService(t, svcCfg(), p)

		d := counterDelta(t, outcome("served"), func() {
			if r := svc.Readings(context.Background(), testLat, testLon); r.Status != StatusOK {
				t.Fatalf("Status = %q, want %q", r.Status, StatusOK)
			}
		})
		if d != 1 {
			t.Errorf("served outcome delta = %v, want 1", d)
		}
	})

	t.Run("served_stale", func(t *testing.T) {
		p := newFakeProvider()
		p.errs[EndpointCurrent] = errors.New("openweather 500")
		svc, cache, _, now := newTestService(t, svcCfg(), p)
		t0 := *now
		fresh := t0.Add(-time.Minute)
		seedReading(t, cache, ReadingKey(testLat, testLon, EndpointCurrent), p.current, t0.Add(-20*time.Minute))
		seedReading(t, cache, ReadingKey(testLat, testLon, EndpointHourly), p.hourly, fresh)
		seedReading(t, cache, ReadingKey(testLat, testLon, EndpointDaily), p.daily, fresh)

		d := counterDelta(t, outcome("served_stale"), func() {
			if r := svc.Readings(context.Background(), testLat, testLon); r.Status != StatusStale {
				t.Fatalf("Status = %q, want %q", r.Status, StatusStale)
			}
		})
		if d != 1 {
			t.Errorf("served_stale outcome delta = %v, want 1", d)
		}
	})
}
