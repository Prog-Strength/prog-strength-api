package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// OpenWeather One Call 4.0 + Geocoding 1.0 provider.
//
// Every method is exactly one metered HTTP call, so the Service's budget
// ledger can reserve per-method. Responses are normalized here — metric
// units requested from the API, wind converted from the API's m/s to the
// canonical km/h — so nothing OpenWeather-shaped ever reaches the cache.
// The 4.0 timeline shapes are pinned by the canned fixtures in
// openweather_test.go; those fixtures, not live responses, are the parse
// contract this file implements.

const openWeatherBaseURL = "https://api.openweathermap.org"

// hourlyBucketCount is how many forecast hours the provider returns. The
// tile shows 5; returning 12 lets the service re-slice without a re-fetch
// when the cached strip's leading hours age out.
const hourlyBucketCount = 12

// Compile-time check that *OpenWeatherProvider satisfies Provider.
var _ Provider = (*OpenWeatherProvider)(nil)

type OpenWeatherProvider struct {
	client *http.Client
	apiKey string

	// baseURL defaults to production OpenWeather; tests point it at an
	// httptest server (the FatSecret provider's URL-override pattern).
	baseURL string
}

func NewOpenWeatherProvider(client *http.Client, apiKey string) *OpenWeatherProvider {
	return &OpenWeatherProvider{
		client:  client,
		apiKey:  apiKey,
		baseURL: openWeatherBaseURL,
	}
}

func (p *OpenWeatherProvider) Configured() bool { return p.apiKey != "" }

// owWeatherTag is the weather[0] condition tag shared by every One Call
// payload; the icon code (e.g. "01d") passes through untranslated.
type owWeatherTag struct {
	Main string `json:"main"`
	Icon string `json:"icon"`
}

func (p *OpenWeatherProvider) Current(ctx context.Context, lat, lon float64) (Current, error) {
	var payload struct {
		Temp      float64        `json:"temp"`
		FeelsLike float64        `json:"feels_like"`
		Humidity  int            `json:"humidity"`
		WindSpeed float64        `json:"wind_speed"`
		Weather   []owWeatherTag `json:"weather"`
	}
	if err := p.get(ctx, "/data/4.0/current", metricParams(lat, lon), &payload); err != nil {
		return Current{}, err
	}
	out := Current{
		TempC:      payload.Temp,
		FeelsLikeC: payload.FeelsLike,
		Humidity:   payload.Humidity,
		// Metric-units wind is m/s; the canonical model is km/h.
		WindKMH: payload.WindSpeed * 3.6,
	}
	// A missing weather tag degrades to empty condition/icon rather than
	// failing the whole reading — the temperatures are still usable.
	if len(payload.Weather) > 0 {
		out.Condition = payload.Weather[0].Main
		out.Icon = payload.Weather[0].Icon
	}
	return out, nil
}

func (p *OpenWeatherProvider) Hourly(ctx context.Context, lat, lon float64) ([]HourlyBucket, error) {
	var payload struct {
		Data []struct {
			DT      int64          `json:"dt"`
			Temp    float64        `json:"temp"`
			Weather []owWeatherTag `json:"weather"`
		} `json:"data"`
	}
	if err := p.get(ctx, "/data/4.0/timeline/1h", metricParams(lat, lon), &payload); err != nil {
		return nil, err
	}
	entries := payload.Data
	if len(entries) > hourlyBucketCount {
		entries = entries[:hourlyBucketCount]
	}
	out := make([]HourlyBucket, 0, len(entries))
	for _, e := range entries {
		b := HourlyBucket{At: unixUTC(e.DT), TempC: e.Temp}
		if len(e.Weather) > 0 {
			b.Icon = e.Weather[0].Icon
		}
		out = append(out, b)
	}
	return out, nil
}

func (p *OpenWeatherProvider) Daily(ctx context.Context, lat, lon float64) (Daily, error) {
	var payload struct {
		Data []struct {
			Sunrise int64 `json:"sunrise"`
			Sunset  int64 `json:"sunset"`
			Temp    struct {
				Max float64 `json:"max"`
				Min float64 `json:"min"`
			} `json:"temp"`
		} `json:"data"`
	}
	if err := p.get(ctx, "/data/4.0/timeline/1day", metricParams(lat, lon), &payload); err != nil {
		return Daily{}, err
	}
	if len(payload.Data) == 0 {
		// Daily promises a single concrete summary; an empty timeline is a
		// provider fault, not a zero-value day.
		return Daily{}, fmt.Errorf("openweather daily: response carried no timeline entries")
	}
	today := payload.Data[0]
	return Daily{
		HighC:   today.Temp.Max,
		LowC:    today.Temp.Min,
		Sunrise: unixUTC(today.Sunrise),
		Sunset:  unixUTC(today.Sunset),
	}, nil
}

// owGeoEntry is the Geocoding 1.0 result shape; "state" is omitted for
// most non-US places, which maps onto GeoResult.State's omitempty.
type owGeoEntry struct {
	Name    string  `json:"name"`
	State   string  `json:"state"`
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

func (p *OpenWeatherProvider) GeocodeDirect(ctx context.Context, query string, limit int) ([]GeoResult, error) {
	params := url.Values{
		"q":     {query},
		"limit": {strconv.Itoa(limit)},
	}
	var payload []owGeoEntry
	if err := p.get(ctx, "/geo/1.0/direct", params, &payload); err != nil {
		return nil, err
	}
	return normalizeGeo(payload), nil
}

func (p *OpenWeatherProvider) GeocodeReverse(ctx context.Context, lat, lon float64) ([]GeoResult, error) {
	// limit=1: reverse lookup exists to label a coordinate, and the tile
	// only ever shows one label per saved location.
	params := latLonParams(lat, lon)
	params.Set("limit", "1")
	var payload []owGeoEntry
	if err := p.get(ctx, "/geo/1.0/reverse", params, &payload); err != nil {
		return nil, err
	}
	return normalizeGeo(payload), nil
}

// get performs one metered API call: appid on every request, units=metric
// on the /data/ endpoints (geocoding has no units), non-200 surfaced as an
// error for the Service to translate into degraded mode.
func (p *OpenWeatherProvider) get(ctx context.Context, path string, params url.Values, dst any) error {
	params.Set("appid", p.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openweather %s: unexpected status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("openweather %s: decode response: %w", path, err)
	}
	return nil
}

// latLonParams is the bare coordinate pair — geocoding sends exactly this.
// The data endpoints layer units=metric on top via metricParams so no
// weather-only parameter ever leaks onto a geocoding call.
func latLonParams(lat, lon float64) url.Values {
	return url.Values{
		"lat": {strconv.FormatFloat(lat, 'f', -1, 64)},
		"lon": {strconv.FormatFloat(lon, 'f', -1, 64)},
	}
}

func metricParams(lat, lon float64) url.Values {
	params := latLonParams(lat, lon)
	params.Set("units", "metric")
	return params
}

func normalizeGeo(entries []owGeoEntry) []GeoResult {
	out := make([]GeoResult, 0, len(entries))
	for _, e := range entries {
		out = append(out, GeoResult(e))
	}
	return out
}

// unixUTC converts OpenWeather's unix-seconds timestamps; UTC keeps cached
// payloads timezone-stable regardless of server locale.
func unixUTC(sec int64) time.Time { return time.Unix(sec, 0).UTC() }
