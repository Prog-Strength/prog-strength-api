package weather

import (
	"context"
	"errors"
	"time"
)

// Provider is the vendor-swap seam, modeled on nutritionlookup.Provider.
// Implementations must be safe for concurrent use, always return METRIC
// values, and surface HTTP failures as errors — the Service decides how to
// degrade. One method per metered endpoint so the budget ledger can reserve
// exactly the calls a refresh will make.
type Provider interface {
	Configured() bool
	Current(ctx context.Context, lat, lon float64) (Current, error)
	Hourly(ctx context.Context, lat, lon float64) ([]HourlyBucket, error)
	Daily(ctx context.Context, lat, lon float64) (Daily, error)
	GeocodeDirect(ctx context.Context, query string, limit int) ([]GeoResult, error)
	GeocodeReverse(ctx context.Context, lat, lon float64) ([]GeoResult, error)
	Historical(ctx context.Context, lat, lon float64, at time.Time) (Observation, error)
}

// ErrNoHistoricalData is the provider's DEFINITIVE negative: there is no
// reading for this time and there never will be (the timestamp predates the
// history window). It is the one terminal failure — the backfill records it as
// `unavailable` and never retries without --retry-unavailable. Every other
// error is transient and leaves no row.
var ErrNoHistoricalData = errors.New("weather: provider has no historical data for the requested time")
