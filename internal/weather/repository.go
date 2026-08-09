package weather

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CacheRow mirrors weather_cache. payload_json holds the normalized reading
// for the endpoint the key names, never the raw provider body.
type CacheRow struct {
	CacheKey    string
	PayloadJSON string
	FetchedAt   time.Time
	LastUsedAt  time.Time
}

type CacheRepository interface {
	Get(ctx context.Context, key string) (*CacheRow, error)
	Put(ctx context.Context, row CacheRow) error
	// LastSuccess is the newest fetched_at across all rows — the durable
	// liveness signal the metrics collector publishes. Zero time when the
	// cache is empty.
	LastSuccess(ctx context.Context) (time.Time, error)
}

type LocationsRepository interface {
	List(ctx context.Context, userID string) ([]Location, error)
	ReplaceAll(ctx context.Context, userID string, locations []Location) error
	Count(ctx context.Context) (int, error) // all users; feeds api_weather_saved_locations
}

// ReadingKey builds the coordinate cache key. 2 decimal places (~1.1 km):
// full precision would fragment the cache — two users, or one user re-running
// "use my location", would produce near-identical coordinates that miss each
// other's cached readings and burn budget for no benefit.
func ReadingKey(lat, lon float64, endpoint Endpoint) string {
	return fmt.Sprintf("%.2f:%.2f:%s", lat, lon, endpoint)
}

func GeocodeDirectKey(query string) string {
	return "geocode_direct:" + normalizeQuery(query)
}

func GeocodeReverseKey(lat, lon float64) string {
	return fmt.Sprintf("geocode_reverse:%.2f:%.2f", lat, lon)
}

// normalizeQuery matches nutritionlookup's normalization: lower-cased,
// whitespace collapsed, so "Denver  CO" and "denver co" share one row.
func normalizeQuery(q string) string {
	return strings.Join(strings.Fields(strings.ToLower(q)), " ")
}
