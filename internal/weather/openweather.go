package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
//
// The historical surface carries its own trap, and one the others do not: its
// readings are stored FOREVER rather than cached for an hour, so a
// silently-wrong decode is not a bad tile that self-heals — it is an immutable
// 0 °C fact attached to a run. It is also the only surface with a definitive
// negative (see ErrNoHistoricalData): a timestamp outside the history window
// answers 400 and no retry will ever change that, while every other failure
// must stay transient so nothing terminal gets recorded.
//
// History is NOT a separate endpoint. One Call 4.0 has no timemachine path;
// history is served by the same /timeline/1h that Hourly calls, with a `start`
// unix timestamp added. That makes Historical the second caller of a path this
// file already speaks, which has one consequence worth stating up front: the
// path can no longer distinguish the two, so anything that must apply to only
// one of them keys on the `start` param instead (see get's 400 handling).

const openWeatherBaseURL = "https://api.openweathermap.org"

// oneCallBasePath prefixes every One Call 4.0 data endpoint. It is a single
// constant rather than three inlined literals because three independent
// copies of the prefix is exactly how the /onecall segment came to be
// missing from all three paths at once.
const oneCallBasePath = "/data/4.0/onecall"

// hourlyTimelinePath serves BOTH the forecast (Hourly) and history
// (Historical) — the latter by adding historicalTimeParam. It is one named
// constant rather than two literals precisely because the two callers must not
// drift apart: this file's previous version gave history its own
// "/timemachine" path, which does not exist in One Call 4.0 and 404'd every
// capture in production while the tests passed against the same wrong path.
//
// Verified by live probe, 2026-08-09 — see
// sows/activity-weather-conditions.md § "Endpoint contract". Corroborated in
// openweather_test.go by the live-captured owHourlyJSON, whose next/prev URLs
// are literally /data/4.0/onecall/timeline/1h?cnt=20&start=…
const hourlyTimelinePath = oneCallBasePath + "/timeline/1h"

// historicalTimeParam turns hourlyTimelinePath into a historical read: unix
// seconds, UTC. ISO 8601 is rejected with 400 {"cod":"400","message":"wrong
// start time"}. The response is 20 hourly buckets from this instant forward,
// with data[0].dt equal to it — so Historical reads data[0] and ignores the
// rest, and never follows next/prev (each is a separately billed call).
//
// It doubles as the discriminator in get: presence of this param is what makes
// a call historical, now that the path alone cannot say.
const historicalTimeParam = "start"

// errorSnippetBytes bounds how much of an error body reaches a log line.
const errorSnippetBytes = 256

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
	if err := p.get(ctx, hourlyTimelinePath, metricParams(lat, lon), &payload); err != nil {
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

// owPrecipBlock is the rain/snow sub-object; both report the millimeters that
// fell during the observation hour under the same "1h" key.
type owPrecipBlock struct {
	OneH float64 `json:"1h"`
}

func (p *OpenWeatherProvider) Historical(ctx context.Context, lat, lon float64, at time.Time) (Observation, error) {
	params := metricParams(lat, lon)
	params.Set(historicalTimeParam, strconv.FormatInt(at.UTC().Unix(), 10))
	var payload struct {
		Data []struct {
			DT        int64          `json:"dt"`
			Temp      float64        `json:"temp"`
			FeelsLike float64        `json:"feels_like"`
			DewPoint  float64        `json:"dew_point"`
			Humidity  int            `json:"humidity"`
			WindSpeed float64        `json:"wind_speed"`
			WindDeg   int            `json:"wind_deg"`
			Rain      owPrecipBlock  `json:"rain"`
			Snow      owPrecipBlock  `json:"snow"`
			Weather   []owWeatherTag `json:"weather"`
		} `json:"data"`
	}
	if err := p.get(ctx, hourlyTimelinePath, params, &payload); err != nil {
		return Observation{}, err
	}
	if len(payload.Data) == 0 {
		// Same guard as Current and Daily, and the reason this file's header
		// calls the envelope load-bearing: decoding the wrong shape succeeds
		// silently and would store a plausible 0 °C reading as a fact.
		return Observation{}, fmt.Errorf("openweather historical: response carried no data entries")
	}
	e := payload.Data[0]
	out := Observation{
		// The provider's own hour, not the requested one, so a reading is
		// never attributed to a timestamp it does not describe.
		ObservedAt: unixUTC(e.DT),
		TempC:      e.Temp,
		FeelsLikeC: e.FeelsLike,
		DewPointC:  e.DewPoint,
		Humidity:   e.Humidity,
		WindKMH:    e.WindSpeed * 3.6, // metric units are m/s; the canonical model is km/h
		WindDeg:    e.WindDeg,
		// Rain and snow are reported separately and are both "water that fell
		// in this hour"; the model carries one total.
		PrecipMM: e.Rain.OneH + e.Snow.OneH,
	}
	// A missing weather tag degrades to empty condition/icon rather than
	// failing the whole reading, exactly as Current does.
	if len(e.Weather) > 0 {
		out.Condition = e.Weather[0].Main
		out.Icon = e.Weather[0].Icon
	}
	return out, nil
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
	if resp.StatusCode == http.StatusBadRequest && params.Has(historicalTimeParam) {
		// OpenWeather answers 400 for a timestamp outside its history window.
		// That is the one failure worth recording as terminal; everything else
		// (5xx, timeouts, 429) is transient and must leave no row.
		//
		// Keyed on the `start` param, NOT on the path. History rides the same
		// /timeline/1h that Hourly calls, so `path == …` would sweep every
		// dashboard-tile 400 into the terminal branch; `start` is present on
		// exactly the historical calls and nothing else.
		//
		// Scoped to the historical surface deliberately. A 400 from /current
		// or geocoding means WE sent a malformed request, and letting that
		// read as a definitive negative would have the backfill write
		// permanent `unavailable` rows to record a bug in our own query
		// string.
		//
		// The vendor's message rides along because this is the ONE error that
		// writes a permanent row: an operator asking why a night of backfill
		// went terminal needs to read "requested time is out of allowed
		// range" rather than infer it. Bounded, because it is untrusted input
		// on its way into a log line.
		return fmt.Errorf("openweather %s: %w: %s", path, ErrNoHistoricalData, errorSnippet(resp.Body))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openweather %s: unexpected status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("openweather %s: decode response: %w", path, err)
	}
	return nil
}

// errorSnippet reads at most errorSnippetBytes of an error response for the
// log line. A read failure yields "" rather than an error of its own — the
// status already carries the outcome, the body is only ever detail.
func errorSnippet(body io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(body, errorSnippetBytes))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
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
