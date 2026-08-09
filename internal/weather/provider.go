package weather

import "context"

// Provider is the vendor-swap seam, modelled on nutritionlookup.Provider.
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
}
