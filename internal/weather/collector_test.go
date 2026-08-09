package weather

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// exporterCfg is the baseline config the collector tests vary: budget 800
// with a hard ceiling of 2000 so the paid-overage case is distinguishable.
func exporterCfg() config.WeatherConfig {
	return config.WeatherConfig{
		Enabled:              true,
		DailyCallBudget:      800,
		AllowPaidOverage:     false,
		DailyCallHardCeiling: 2000,
	}
}

// newSeededExporter builds an Exporter over one migrated DB with: `used`
// calls reserved today, one cache row fetched at fetchedAt (skipped when
// zero), and two saved locations for user-a.
func newSeededExporter(t *testing.T, cfg config.WeatherConfig, used int, fetchedAt time.Time) (*Exporter, *sql.DB) {
	t.Helper()
	database := dbtest.New(t)
	ctx := context.Background()

	ledger := NewBudgetLedger(database)
	fixLedgerClock(ledger, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	if used > 0 {
		if err := ledger.Reserve(ctx, used, used); err != nil {
			t.Fatalf("seed %d used calls: %v", used, err)
		}
	}

	cache := NewSQLiteCacheRepository(database)
	if !fetchedAt.IsZero() {
		row := CacheRow{
			CacheKey:    "39.74:-104.98:current",
			PayloadJSON: `{"temp_c":21.4}`,
			FetchedAt:   fetchedAt,
			LastUsedAt:  fetchedAt,
		}
		if err := cache.Put(ctx, row); err != nil {
			t.Fatalf("seed cache row: %v", err)
		}
	}

	mustInsertUser(t, database, "user-a")
	locations := NewSQLiteLocationsRepository(database)
	if err := locations.ReplaceAll(ctx, "user-a", denverBoulder()); err != nil {
		t.Fatalf("seed locations: %v", err)
	}

	return NewExporter(cfg, ledger, cache, locations), database
}

// TestExporterRefreshPublishesDurableState is the happy path: every gauge
// reflects what SQLite says, not what process memory remembers.
func TestExporterRefreshPublishesDurableState(t *testing.T) {
	fetchedAt := time.Date(2026, 8, 9, 11, 30, 0, 0, time.UTC)
	e, _ := newSeededExporter(t, exporterCfg(), 400, fetchedAt)

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"calls_used_today", testutil.ToFloat64(callsUsedTodayGauge), 400},
		{"daily_budget", testutil.ToFloat64(dailyBudgetGauge), 800},
		{"budget_utilization_ratio", testutil.ToFloat64(budgetUtilizationGauge), 0.5},
		{"shutoff_active", testutil.ToFloat64(shutoffActiveGauge), 0},
		{"last_success_timestamp_seconds", testutil.ToFloat64(lastSuccessGauge), float64(fetchedAt.Unix())},
		{"enabled", testutil.ToFloat64(enabledGauge), 1},
		{"saved_locations", testutil.ToFloat64(savedLocationsGauge), 2},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("api_weather_%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestExporterZeroBudgetPublishesZeroUtilization pins the divide-by-zero
// guard: a misconfigured budget of 0 must yield utilization 0, not a panic
// or NaN. Handled once here in Go so five alert rules don't each need a
// clamp expression.
func TestExporterZeroBudgetPublishesZeroUtilization(t *testing.T) {
	cfg := exporterCfg()
	cfg.DailyCallBudget = 0
	e, _ := newSeededExporter(t, cfg, 400, time.Time{})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(budgetUtilizationGauge); got != 0 {
		t.Errorf("utilization with zero budget = %v, want 0", got)
	}
	// A zero ceiling also must not report shutoff: nothing was exhausted,
	// the feature is just misconfigured.
	if got := testutil.ToFloat64(shutoffActiveGauge); got != 0 {
		t.Errorf("shutoff_active with zero budget = %v, want 0", got)
	}
	// last_success publishes a literal 0 when the cache has never been
	// written, matching the WHOOP liveness gauge's absent-vs-zero choice.
	if got := testutil.ToFloat64(lastSuccessGauge); got != 0 {
		t.Errorf("last_success with empty cache = %v, want 0", got)
	}
}

// TestExporterShutoffAtCeiling: used == ceiling flips the shutoff gauge.
func TestExporterShutoffAtCeiling(t *testing.T) {
	e, _ := newSeededExporter(t, exporterCfg(), 800, time.Time{})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(shutoffActiveGauge); got != 1 {
		t.Errorf("shutoff_active at ceiling = %v, want 1", got)
	}
	if got := testutil.ToFloat64(budgetUtilizationGauge); got != 1 {
		t.Errorf("utilization at ceiling = %v, want 1", got)
	}
}

// TestExporterPaidOverageReportsHardCeiling: with paid overage allowed the
// active ceiling — and therefore the published budget — is the hard ceiling.
func TestExporterPaidOverageReportsHardCeiling(t *testing.T) {
	cfg := exporterCfg()
	cfg.AllowPaidOverage = true
	e, _ := newSeededExporter(t, cfg, 400, time.Time{})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(dailyBudgetGauge); got != 2000 {
		t.Errorf("daily_budget with paid overage = %v, want hard ceiling 2000", got)
	}
	if got := testutil.ToFloat64(budgetUtilizationGauge); got != 0.2 {
		t.Errorf("utilization with paid overage = %v, want 400/2000 = 0.2", got)
	}
}

// TestExporterDisabledFlag: the enabled gauge mirrors the feature flag so
// alerts can silence themselves while the tile is off.
func TestExporterDisabledFlag(t *testing.T) {
	cfg := exporterCfg()
	cfg.Enabled = false
	e, _ := newSeededExporter(t, cfg, 0, time.Time{})

	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := testutil.ToFloat64(enabledGauge); got != 0 {
		t.Errorf("enabled gauge with flag off = %v, want 0", got)
	}
}

// TestExporterReadErrorKeepsPreviousValues: a failing durable read must not
// crash the refresh loop, and — mirroring whoopadmin — the gauges keep their
// last-published values rather than dropping to misleading zeros.
func TestExporterReadErrorKeepsPreviousValues(t *testing.T) {
	fetchedAt := time.Date(2026, 8, 9, 11, 30, 0, 0, time.UTC)
	e, database := newSeededExporter(t, exporterCfg(), 400, fetchedAt)

	// One good refresh establishes known values...
	if err := e.refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	// ...then the store goes away.
	if err := database.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if err := e.refresh(context.Background()); err == nil {
		t.Fatal("refresh over closed DB = nil error, want error")
	}

	if got := testutil.ToFloat64(callsUsedTodayGauge); got != 400 {
		t.Errorf("calls_used_today after failed refresh = %v, want previous 400", got)
	}
	if got := testutil.ToFloat64(lastSuccessGauge); got != float64(fetchedAt.Unix()) {
		t.Errorf("last_success after failed refresh = %v, want previous %v", got, float64(fetchedAt.Unix()))
	}
	if got := testutil.ToFloat64(savedLocationsGauge); got != 2 {
		t.Errorf("saved_locations after failed refresh = %v, want previous 2", got)
	}
}
