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
// The 4.0 shapes are pinned by fixtures in openweather_test.go that are
// CAPTURED FROM LIVE RESPONSES; re-capture rather than hand-edit them.
//
// Every One Call 4.0 data endpoint hangs off oneCallBasePath, and every 4.0
// response wraps its readings in a top-level `data` array — including
// /current, which returns a one-element array rather than a bare object.
// Both facts were originally missed here (paths omitted /onecall, and
// Current parsed the root), which 404'd every reading in production.
//
// Responses also carry fields this provider deliberately ignores: next/prev
// pagination URLs, alerts, pop, wind_gust, and the lunar block. Following a
// pagination link is a separately BILLED call, so the first page is always
// the whole answer as far as this file is concerned.

const openWeatherBaseURL = "https://api.openweathermap.org"

// oneCallBasePath prefixes every One Call 4.0 data endpoint. It is a single
// constant rather than three inlined literals because three independent
// copies of the prefix is exactly how the /onecall segment came to be
// missing from all three paths at once.
const oneCallBasePath = "/data/4.0/onecall"

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
	// data[0], not the root: /current returns the same envelope as the
	// timelines, carrying a single-element array. Parsing the root decodes
	// into a zero-value struct WITHOUT error — encoding/json simply ignores
	// absent fields — so this shape is load-bearing. Getting it wrong yields
	// a plausible-looking 0°C reading rather than a failure.
	var payload struct {
		Data []struct {
			Temp      float64        `json:"temp"`
			FeelsLike float64        `json:"feels_like"`
			Humidity  int            `json:"humidity"`
			WindSpeed float64        `json:"wind_speed"`
			Weather   []owWeatherTag `json:"weather"`
		} `json:"data"`
	}
	if err := p.get(ctx, oneCallBasePath+"/current", metricParams(lat, lon), &payload); err != nil {
		return Current{}, err
	}
	if len(payload.Data) == 0 {
		// Same reasoning as Daily's empty-timeline guard: an empty array is
		// a provider fault, and returning the zero value would cache 0°C as
		// a real observation.
		return Current{}, fmt.Errorf("openweather current: response carried no data entries")
	}
	entry := payload.Data[0]
	out := Current{
		TempC:      entry.Temp,
		FeelsLikeC: entry.FeelsLike,
		Humidity:   entry.Humidity,
		// Metric-units wind is m/s; the canonical model is km/h.
		WindKMH: entry.WindSpeed * 3.6,
	}
	// A missing weather tag degrades to empty condition/icon rather than
	// failing the whole reading — the temperatures are still usable.
	if len(entry.Weather) > 0 {
		out.Condition = entry.Weather[0].Main
		out.Icon = entry.Weather[0].Icon
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
	if err := p.get(ctx, oneCallBasePath+"/timeline/1h", metricParams(lat, lon), &payload); err != nil {
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
	if err := p.get(ctx, oneCallBasePath+"/timeline/1day", metricParams(lat, lon), &payload); err != nil {
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
