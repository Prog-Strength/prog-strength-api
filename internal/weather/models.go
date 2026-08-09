package weather

import "time"

// Endpoint names one metered provider surface. They double as cache-key
// suffixes and metric label values, so the set is closed and lowercase.
type Endpoint string

const (
	EndpointCurrent        Endpoint = "current"
	EndpointHourly         Endpoint = "hourly"
	EndpointDaily          Endpoint = "daily"
	EndpointGeocodeDirect  Endpoint = "geocode_direct"
	EndpointGeocodeReverse Endpoint = "geocode_reverse"
)

// Current is the normalized current-conditions reading, metric units.
// This is what weather_cache stores for the "current" endpoint — never the
// raw provider body, so a vendor swap can't leak provider shapes into cache.
type Current struct {
	TempC      float64 `json:"temp_c"`
	FeelsLikeC float64 `json:"feels_like_c"`
	Humidity   int     `json:"humidity"`
	WindKMH    float64 `json:"wind_kmh"`
	Condition  string  `json:"condition"`
	Icon       string  `json:"icon"`
}

// HourlyBucket is one hour of the forecast strip.
type HourlyBucket struct {
	At    time.Time `json:"at"`
	TempC float64   `json:"temp_c"`
	Icon  string    `json:"icon"`
}

// Daily is today's summary from the daily timeline.
type Daily struct {
	HighC   float64   `json:"high_c"`
	LowC    float64   `json:"low_c"`
	Sunrise time.Time `json:"sunrise"`
	Sunset  time.Time `json:"sunset"`
}

// GeoResult is one geocoding candidate (direct search or reverse).
type GeoResult struct {
	Name    string  `json:"name"`
	State   string  `json:"state,omitempty"`
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

// Location is a user's saved place, ordered by Position.
type Location struct {
	ID        string
	UserID    string
	Position  int
	Label     string
	Country   string
	State     *string
	Lat       float64
	Lon       float64
	CreatedAt time.Time
}
