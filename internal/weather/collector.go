package weather

import (
	"context"
	"log/slog"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
)

const refreshInterval = 5 * time.Minute

// Exporter periodically publishes the api_weather_* gauges from durable
// SQLite state (metrics.go explains why durability matters — the WHOOP
// counter postmortem).
//
// Design constraint, load-bearing for the SOW: the exporter only READS
// SQLite. It never calls the provider, so it cannot spend budget and does
// not violate the "no background polling" non-goal; and metrics never
// enforce anything — the service enforces the budget, the exporter only
// reports what the ledger already recorded.
type Exporter struct {
	cfg       config.WeatherConfig
	ledger    *BudgetLedger
	cache     CacheRepository
	locations LocationsRepository
}

func NewExporter(cfg config.WeatherConfig, ledger *BudgetLedger, cache CacheRepository, locations LocationsRepository) *Exporter {
	return &Exporter{cfg: cfg, ledger: ledger, cache: cache, locations: locations}
}

// Run refreshes immediately, then every refreshInterval, until ctx is
// cancelled. Same shape as whoopadmin's ConnectionsExporter.Run.
func (e *Exporter) Run(ctx context.Context) {
	if err := e.refresh(ctx); err != nil {
		slog.WarnContext(ctx, "weather: gauge refresh failed", "error", err)
	}
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.refresh(ctx); err != nil {
				slog.WarnContext(ctx, "weather: gauge refresh failed", "error", err)
			}
		}
	}
}

// refresh reads all durable state first and publishes only after every read
// succeeded: a failed read returns the error (Run logs it) and the gauges
// keep their previous values, so a transient DB hiccup can't publish
// misleading zeros to the budget alerts.
func (e *Exporter) refresh(ctx context.Context) error {
	used, err := e.ledger.UsedToday(ctx)
	if err != nil {
		return err
	}
	lastSuccess, err := e.cache.LastSuccess(ctx)
	if err != nil {
		return err
	}
	locationCount, err := e.locations.Count(ctx)
	if err != nil {
		return err
	}

	// The dashboard must draw thresholds against the same ceiling the
	// service enforces — activeCeiling is the single source (budget.go),
	// otherwise "100% utilized" could show while calls still flow.
	ceiling := activeCeiling(e.cfg)

	callsUsedTodayGauge.Set(float64(used))
	dailyBudgetGauge.Set(float64(ceiling))

	// Divide-by-zero handled once here, not in five alert rules: a
	// misconfigured ceiling of 0 publishes utilization 0 (and no shutoff —
	// nothing was exhausted, the feature is misconfigured).
	if ceiling > 0 {
		budgetUtilizationGauge.Set(float64(used) / float64(ceiling))
	} else {
		budgetUtilizationGauge.Set(0)
	}
	if used >= ceiling && ceiling > 0 {
		shutoffActiveGauge.Set(1)
	} else {
		shutoffActiveGauge.Set(0)
	}

	// 0 (not absent) when the cache has never been written, mirroring the
	// WHOOP liveness gauge: an enabled integration that has never fetched
	// should read as infinitely stale, not unknown.
	if lastSuccess.IsZero() {
		lastSuccessGauge.Set(0)
	} else {
		lastSuccessGauge.Set(float64(lastSuccess.Unix()))
	}

	if e.cfg.Enabled {
		enabledGauge.Set(1)
	} else {
		enabledGauge.Set(0)
	}
	savedLocationsGauge.Set(float64(locationCount))
	return nil
}
