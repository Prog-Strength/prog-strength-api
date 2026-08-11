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
	EndpointHistorical     Endpoint = "historical"
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

// Observation is one historical reading at a specific hour, metric units.
// Deliberately NOT Current: Current is the tile's shape and carries no dew
// point, wind direction, or precipitation, and no observation timestamp —
// for a forecast "now" is implicit, for a stored reading it is the point.
type Observation struct {
	ObservedAt time.Time `json:"observed_at"`
	TempC      float64   `json:"temp_c"`
	FeelsLikeC float64   `json:"feels_like_c"`
	DewPointC  float64   `json:"dew_point_c"`
	Humidity   int       `json:"humidity"`
	WindKMH    float64   `json:"wind_kmh"`
	WindDeg    int       `json:"wind_deg"`
	PrecipMM   float64   `json:"precip_mm"`
	Condition  string    `json:"condition"`
	Icon       string    `json:"icon"`
}

// HourlyBucket is one hour of the forecast strip.
type HourlyBucket struct {
	At    time.Time `json:"at"`
	TempC float64   `json:"temp_c"`
	Icon  string    `json:"icon"`
}

// DayForecast is one day of the multi-day timeline, metric units.
//
// It exists because the /timeline/1day response the provider already bills us
// for carries ~10 days and we were reading data[0] and throwing the rest away.
// A week of forecast is therefore FREE — same call, same budget reservation,
// same cache row — which is the whole reason the tile can offer a week view
// without moving the daily call budget at all.
//
// Every field below is present in the live captured response (see
// openweather_test.go's owDailyJSON). PrecipChance is the provider's `pop`,
// carried through as its native 0..1 probability; the handler renders it as a
// percentage rather than the model pretending to a unit it does not have.
type DayForecast struct {
	At           time.Time `json:"at"`
	HighC        float64   `json:"high_c"`
	LowC         float64   `json:"low_c"`
	Condition    string    `json:"condition"`
	Icon         string    `json:"icon"`
	PrecipChance float64   `json:"precip_chance"`
	WindKMH      float64   `json:"wind_kmh"`
	Humidity     int       `json:"humidity"`
	Sunrise      time.Time `json:"sunrise"`
	Sunset       time.Time `json:"sunset"`
}

// Daily is the daily timeline: today's summary, plus the days behind it.
//
// The four scalar fields are today's and are kept as their own fields rather
// than being read off Days[0]: they are the tile's shipped contract, and a
// provider response whose first entry is malformed should cost the week view,
// not the high/low the tile has always shown.
type Daily struct {
	HighC   float64   `json:"high_c"`
	LowC    float64   `json:"low_c"`
	Sunrise time.Time `json:"sunrise"`
	Sunset  time.Time `json:"sunset"`
	// Days is today-first and may be empty on a cache row written before this
	// field existed; the service treats such a row as outdated and refetches.
	Days []DayForecast `json:"days,omitempty"`
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
