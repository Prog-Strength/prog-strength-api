package weather

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These canned bodies are CAPTURED FROM LIVE OpenWeather One Call 4.0
// responses (Denver, units=metric, 2026-08-09), trimmed only to scrub the
// appid the API echoes back inside its next/prev pagination URLs.
//
// They are captures, not constructions, and that distinction is the whole
// reason this file was rewritten. The previous fixtures were authored to
// match what the provider code assumed — flat fields at the root, and paths
// without the /onecall segment — so the suite asserted the implementation
// against itself and passed while production 404'd on every reading. A
// fixture invented from documentation proves only that the parser agrees
// with its author. Re-capture from the live API when updating these.

const owCurrentJSON = `{
	"lat": 39.74,
	"lon": -104.98,
	"timezone": "America/Denver",
	"timezone_offset": -21600,
	"data": [
		{
			"dt": 1786292561,
			"sunrise": 1786277186,
			"sunset": 1786327469,
			"temp": 31.8,
			"feels_like": 29.85,
			"pressure": 1011,
			"humidity": 21,
			"dew_point": 6.8,
			"uvi": 3.82,
			"clouds": 35,
			"visibility": 10000,
			"wind_speed": 0.45,
			"wind_deg": 84,
			"wind_gust": 1.79,
			"weather": [{"id": 802, "main": "Clouds", "description": "scattered clouds", "icon": "03d"}],
			"alerts": ["urn:oid:2.49.0.1.840.0.SCRUBBED.001.1:SCRUBBED"]
		}
	]
}`

// owHourlyEntry is one bucket in the shape the live /timeline/1h returns —
// every field the real response carries, including the pop and alerts the
// provider deliberately ignores. Parameterized over dt/temp/icon so
// owHourlyJSON can emit 14 buckets and make the 12-bucket truncation
// observable; the live response pages at 20.
func owHourlyEntry(dt int64, temp float64, icon string) string {
	return fmt.Sprintf(`{
		"dt": %d,
		"temp": %g,
		"feels_like": 30.01,
		"pressure": 1011,
		"humidity": 20,
		"dew_point": 6.28,
		"uvi": 3.82,
		"clouds": 35,
		"visibility": 10000,
		"wind_speed": 1.15,
		"wind_deg": 196,
		"wind_gust": 1.11,
		"weather": [{"id": 802, "main": "Clouds", "description": "scattered clouds", "icon": %q}],
		"pop": 0,
		"alerts": ["urn:oid:2.49.0.1.840.0.SCRUBBED.001.1:SCRUBBED"]
	}`, dt, temp, icon)
}

// owHourlyJSON carries 14 buckets so the 12-bucket truncation is observable.
// next/prev are present because the live response always carries them — the
// provider must keep ignoring them, since following a page is a billed call.
func owHourlyJSON() string {
	var entries []string
	for i := 0; i < 14; i++ {
		entries = append(entries, owHourlyEntry(
			1754740800+int64(i)*3600, 20.5+float64(i), fmt.Sprintf("0%dd", (i%9)+1)))
	}
	return `{
		"lat": 39.74, "lon": -104.98,
		"timezone": "America/Denver", "timezone_offset": -21600,
		"next": "http://api.openweathermap.org/data/4.0/onecall/timeline/1h?cnt=20&start=1786363200&appid=SCRUBBED",
		"prev": "http://api.openweathermap.org/data/4.0/onecall/timeline/1h?cnt=20&start=1786219200&appid=SCRUBBED",
		"data": [` + strings.Join(entries, ",") + `]}`
}

// owDailyJSON is the live /timeline/1day shape: temp as an object of
// day/min/max/night/eve/morn, alongside sunrise/sunset and the lunar block.
const owDailyJSON = `{
	"lat": 39.74, "lon": -104.98,
	"timezone": "America/Denver", "timezone_offset": -21600,
	"next": "http://api.openweathermap.org/data/4.0/onecall/timeline/1day?cnt=10&start=1787097600&appid=SCRUBBED",
	"prev": "http://api.openweathermap.org/data/4.0/onecall/timeline/1day?cnt=10&start=1785369600&appid=SCRUBBED",
	"data": [{
		"dt": 1786233600,
		"sunrise": 1786277186,
		"sunset": 1786327469,
		"moonrise": 1786172520,
		"moonset": 1786230660,
		"moon_phase": 0.85,
		"temp": {"day": 35.45, "min": 24.74, "max": 37.1, "night": 28.54, "eve": 32.14, "morn": 24.74},
		"feels_like": {"day": 35.45, "night": 28.54, "eve": 32.14, "morn": 24.74},
		"pressure": 1008.47,
		"humidity": 7,
		"wind_speed": 8.34,
		"wind_deg": 330,
		"weather": [{"id": 803, "main": "Clouds", "description": "broken clouds", "icon": "04d"}],
		"clouds": 56,
		"pop": 0.07,
		"uvi": 0
	}]
}`

// ---------------------------------------------------------------------------
// HISTORICAL ("timemachine") FIXTURES — NOT LIVE CAPTURES.
//
// Unlike every fixture above, the owHistorical* bodies below are HAND-AUTHORED
// to the DOCUMENTED One Call timemachine envelope. No OpenWeather key was
// available to the change that added them, so nothing here has been observed
// coming out of the live API. They therefore prove only that the parser agrees
// with the documentation — exactly the weaker guarantee the header above warns
// about.
//
// RE-CAPTURE BEFORE ROLLOUT: probe /data/4.0/onecall/timemachine once with the
// production key and replace these bodies with the real response, together
// with the path constant in openweather.go if it turns out to differ.
//
// They still earn their place: the shape they pin is the one the provider must
// fail loudly on when wrong, and the empty-array and root-shape guards below
// are independent of whether this exact envelope is correct.

const owHistoricalJSON = `{
	"lat": 39.74,
	"lon": -104.98,
	"timezone": "America/Denver",
	"timezone_offset": -21600,
	"data": [
		{
			"dt": 1786291200,
			"sunrise": 1786277186,
			"sunset": 1786327469,
			"temp": 18.24,
			"feels_like": 17.61,
			"pressure": 1011,
			"humidity": 44,
			"dew_point": 6.12,
			"uvi": 3.82,
			"clouds": 35,
			"visibility": 10000,
			"wind_speed": 3.5,
			"wind_deg": 210,
			"wind_gust": 6.2,
			"weather": [{"id": 800, "main": "Clear", "description": "clear sky", "icon": "01d"}]
		}
	]
}`

// Rain and snow in the same hour: the model carries one precipitation total,
// so both sub-objects have to be read and summed rather than either winning.
const owHistoricalPrecipJSON = `{
	"lat": 39.74, "lon": -104.98,
	"data": [{
		"dt": 1786291200,
		"temp": 0.5,
		"feels_like": -3.2,
		"dew_point": -0.4,
		"humidity": 92,
		"wind_speed": 3.5,
		"wind_deg": 210,
		"rain": {"1h": 0.42},
		"snow": {"1h": 0.13},
		"weather": [{"id": 616, "main": "Snow", "description": "rain and snow", "icon": "13d"}]
	}]
}`

const owHistoricalNoWeatherJSON = `{
	"lat": 39.74, "lon": -104.98,
	"data": [{
		"dt": 1786291200,
		"temp": 18.24,
		"feels_like": 17.61,
		"dew_point": 6.12,
		"humidity": 44,
		"wind_speed": 3.5,
		"wind_deg": 210,
		"weather": []
	}]
}`

// The PR #124 regression shape: readings at the ROOT with no `data` wrapper.
// encoding/json ignores absent fields, so this decodes into a zero-value
// struct WITHOUT error and would store a plausible 0 °C reading as a fact.
const owHistoricalRootShapeJSON = `{
	"lat": 39.74, "lon": -104.98,
	"dt": 1786291200,
	"temp": 18.24,
	"feels_like": 17.61,
	"dew_point": 6.12,
	"humidity": 44,
	"wind_speed": 3.5,
	"wind_deg": 210,
	"weather": [{"id": 800, "main": "Clear", "description": "clear sky", "icon": "01d"}]
}`

const owGeoDirectJSON = `[
	{"name": "Denver", "lat": 39.7392, "lon": -104.9847, "country": "US", "state": "Colorado"},
	{"name": "Denver", "lat": 35.5312, "lon": -81.0298, "country": "US", "state": "North Carolina"}
]`

// No "state" field: geocoding outside the US commonly omits it.
const owGeoReverseJSON = `[
	{"name": "London", "lat": 51.5073, "lon": -0.1276, "country": "GB"}
]`

// owServer records the last request and serves body for the given path.
func owServer(t *testing.T, wantPath, body string) (*httptest.Server, *url.Values, *string) {
	t.Helper()
	var gotQuery url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		if r.URL.Path != wantPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotQuery, &gotPath
}

func newTestProvider(srv *httptest.Server) *OpenWeatherProvider {
	p := NewOpenWeatherProvider(srv.Client(), "test-key")
	p.baseURL = srv.URL
	return p
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func assertCommonParams(t *testing.T, q url.Values, wantLat, wantLon string) {
	t.Helper()
	if got := q.Get("lat"); got != wantLat {
		t.Errorf("lat param = %q, want %q", got, wantLat)
	}
	if got := q.Get("lon"); got != wantLon {
		t.Errorf("lon param = %q, want %q", got, wantLon)
	}
	if got := q.Get("appid"); got != "test-key" {
		t.Errorf("appid param = %q, want test-key", got)
	}
	if got := q.Get("units"); got != "metric" {
		t.Errorf("units param = %q, want metric", got)
	}
}

func TestOpenWeatherCurrent(t *testing.T) {
	srv, query, path := owServer(t, "/data/4.0/onecall/current", owCurrentJSON)
	p := newTestProvider(srv)

	got, err := p.Current(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	if *path != "/data/4.0/onecall/current" {
		t.Errorf("path = %q, want /data/4.0/onecall/current", *path)
	}
	assertCommonParams(t, *query, "39.7392", "-104.9847")

	// Every expectation below is a value from data[0] of the captured
	// response. A parser reading the ROOT — as this one did — decodes into
	// a zero-value struct WITHOUT error, because encoding/json ignores
	// absent fields. That is the failure this test exists to catch: it is
	// asserting non-zero values, not merely asserting err == nil.
	if got.TempC != 31.8 {
		t.Errorf("TempC = %v, want 31.8 (from data[0], not the root)", got.TempC)
	}
	if got.FeelsLikeC != 29.85 {
		t.Errorf("FeelsLikeC = %v, want 29.85", got.FeelsLikeC)
	}
	if got.Humidity != 21 {
		t.Errorf("Humidity = %v, want 21", got.Humidity)
	}
	// OpenWeather metric wind is m/s; normalized model is km/h (0.45 * 3.6).
	if !almostEqual(got.WindKMH, 1.62) {
		t.Errorf("WindKMH = %v, want 1.62", got.WindKMH)
	}
	if got.Condition != "Clouds" {
		t.Errorf("Condition = %q, want Clouds", got.Condition)
	}
	if got.Icon != "03d" {
		t.Errorf("Icon = %q, want 03d", got.Icon)
	}
}

// A response whose data array is empty must be an error, not a zero-value
// reading. Without this guard the service would cache 0°C/blank-condition
// as a legitimate answer and the tile would render it as fact — the same
// silent-zero failure mode that reading the root produced, arriving by a
// different route. Mirrors the existing empty-timeline guard in Daily.
func TestOpenWeatherCurrentEmptyDataArrayErrors(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/onecall/current", `{"lat": 39.74, "lon": -104.98, "data": []}`)
	p := newTestProvider(srv)

	if _, err := p.Current(context.Background(), 39.7392, -104.9847); err == nil {
		t.Fatal("Current with empty data array: want error, got nil")
	}
}

// The empty-weather-array fixtures pin the documented degradation in
// openweather.go: a reading with no weather tag must keep its temperatures
// and degrade Condition/Icon to empty rather than failing the whole call.
const owCurrentNoWeatherJSON = `{
	"lat": 39.74, "lon": -104.98, "timezone": "America/Denver",
	"data": [{
		"dt": 1786292561,
		"temp": 31.8,
		"feels_like": 29.85,
		"humidity": 21,
		"wind_speed": 0.45,
		"weather": []
	}]
}`

func TestOpenWeatherCurrentEmptyWeatherArrayDegrades(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/onecall/current", owCurrentNoWeatherJSON)
	p := newTestProvider(srv)

	got, err := p.Current(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Current with empty weather array: %v", err)
	}
	if got.TempC != 31.8 {
		t.Errorf("TempC = %v, want 31.8 (temperatures must survive)", got.TempC)
	}
	if !almostEqual(got.WindKMH, 1.62) {
		t.Errorf("WindKMH = %v, want 1.62", got.WindKMH)
	}
	if got.Condition != "" {
		t.Errorf("Condition = %q, want empty when weather array is empty", got.Condition)
	}
	if got.Icon != "" {
		t.Errorf("Icon = %q, want empty when weather array is empty", got.Icon)
	}
}

func TestOpenWeatherHourlyEmptyWeatherArrayDegrades(t *testing.T) {
	body := `{"data": [{"dt": 1754740800, "temp": 20.5, "weather": []}]}`
	srv, _, _ := owServer(t, "/data/4.0/onecall/timeline/1h", body)
	p := newTestProvider(srv)

	got, err := p.Hourly(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Hourly with empty weather array: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(buckets) = %d, want 1", len(got))
	}
	if got[0].TempC != 20.5 {
		t.Errorf("TempC = %v, want 20.5 (temperatures must survive)", got[0].TempC)
	}
	if got[0].Icon != "" {
		t.Errorf("Icon = %q, want empty when weather array is empty", got[0].Icon)
	}
}

func TestOpenWeatherHourlyTruncatesToTwelveBuckets(t *testing.T) {
	srv, query, path := owServer(t, "/data/4.0/onecall/timeline/1h", owHourlyJSON())
	p := newTestProvider(srv)

	got, err := p.Hourly(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Hourly: %v", err)
	}

	if *path != "/data/4.0/onecall/timeline/1h" {
		t.Errorf("path = %q, want /data/4.0/onecall/timeline/1h", *path)
	}
	assertCommonParams(t, *query, "39.7392", "-104.9847")

	if len(got) != 12 {
		t.Fatalf("len(buckets) = %d, want 12 (14 served, next 12 kept)", len(got))
	}
	first := got[0]
	if wantAt := time.Unix(1754740800, 0).UTC(); !first.At.Equal(wantAt) {
		t.Errorf("first At = %v, want %v", first.At, wantAt)
	}
	if first.TempC != 20.5 {
		t.Errorf("first TempC = %v, want 20.5", first.TempC)
	}
	if first.Icon != "01d" {
		t.Errorf("first Icon = %q, want 01d", first.Icon)
	}
	last := got[11]
	if wantAt := time.Unix(1754740800+11*3600, 0).UTC(); !last.At.Equal(wantAt) {
		t.Errorf("last At = %v, want %v", last.At, wantAt)
	}
	if last.TempC != 31.5 {
		t.Errorf("last TempC = %v, want 31.5", last.TempC)
	}
}

func TestOpenWeatherDaily(t *testing.T) {
	srv, query, path := owServer(t, "/data/4.0/onecall/timeline/1day", owDailyJSON)
	p := newTestProvider(srv)

	got, err := p.Daily(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}

	if *path != "/data/4.0/onecall/timeline/1day" {
		t.Errorf("path = %q, want /data/4.0/onecall/timeline/1day", *path)
	}
	assertCommonParams(t, *query, "39.7392", "-104.9847")

	// max/min off the temp OBJECT — the daily timeline reports temp as
	// {day,min,max,night,eve,morn}, not a scalar.
	if got.HighC != 37.1 {
		t.Errorf("HighC = %v, want 37.1", got.HighC)
	}
	if got.LowC != 24.74 {
		t.Errorf("LowC = %v, want 24.74", got.LowC)
	}
	// 12:06:26Z and 02:04:29Z are 06:06 and 20:04 in Denver (UTC-6) — real
	// August sunrise/sunset there, which is what makes data[0] verifiably
	// TODAY rather than an adjacent day's bucket.
	if want := time.Unix(1786277186, 0).UTC(); !got.Sunrise.Equal(want) {
		t.Errorf("Sunrise = %v, want %v", got.Sunrise, want)
	}
	if want := time.Unix(1786327469, 0).UTC(); !got.Sunset.Equal(want) {
		t.Errorf("Sunset = %v, want %v", got.Sunset, want)
	}
}

func TestOpenWeatherDailyEmptyTimelineErrors(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/onecall/timeline/1day", `{"data": []}`)
	p := newTestProvider(srv)

	if _, err := p.Daily(context.Background(), 39.7, -104.9); err == nil {
		t.Fatal("Daily with empty timeline: want error, got nil")
	}
}

// historicalAt is the timestamp every Historical test asks for. It is
// deliberately NOT on an hour boundary: rounding belongs to the capturer, and
// asking for 14:40 pins that the provider forwards the caller's instant
// verbatim rather than quietly rounding it a second time.
var historicalAt = time.Date(2026, 8, 9, 14, 40, 0, 0, time.UTC)

func TestOpenWeatherHistorical(t *testing.T) {
	srv, query, path := owServer(t, "/data/4.0/onecall/timemachine", owHistoricalJSON)
	p := newTestProvider(srv)

	got, err := p.Historical(context.Background(), 39.7392, -104.9847, historicalAt)
	if err != nil {
		t.Fatalf("Historical: %v", err)
	}

	if *path != "/data/4.0/onecall/timemachine" {
		t.Errorf("path = %q, want /data/4.0/onecall/timemachine", *path)
	}
	assertCommonParams(t, *query, "39.7392", "-104.9847")
	// dt is the whole reason this endpoint is not just another timeline read:
	// unix seconds, UTC, and the provider must not be asked for "now".
	if want := strconv.FormatInt(historicalAt.Unix(), 10); query.Get("dt") != want {
		t.Errorf("dt param = %q, want %q", query.Get("dt"), want)
	}

	// Every expectation is a value from data[0]. A parser reading the ROOT
	// decodes into a zero-value struct WITHOUT error, so asserting non-zero
	// values is the point — err == nil alone proves nothing here.
	if want := time.Unix(1786291200, 0).UTC(); !got.ObservedAt.Equal(want) {
		t.Errorf("ObservedAt = %v, want %v (the provider's hour, not the request's)", got.ObservedAt, want)
	}
	if got.TempC != 18.24 {
		t.Errorf("TempC = %v, want 18.24", got.TempC)
	}
	if got.FeelsLikeC != 17.61 {
		t.Errorf("FeelsLikeC = %v, want 17.61", got.FeelsLikeC)
	}
	if got.DewPointC != 6.12 {
		t.Errorf("DewPointC = %v, want 6.12", got.DewPointC)
	}
	if got.Humidity != 44 {
		t.Errorf("Humidity = %v, want 44", got.Humidity)
	}
	// OpenWeather metric wind is m/s; the canonical model is km/h (3.5 * 3.6).
	if !almostEqual(got.WindKMH, 12.6) {
		t.Errorf("WindKMH = %v, want 12.6 (m/s converted)", got.WindKMH)
	}
	if got.WindDeg != 210 {
		t.Errorf("WindDeg = %v, want 210", got.WindDeg)
	}
	// No rain or snow block at all is a dry hour, not a missing measurement.
	if got.PrecipMM != 0 {
		t.Errorf("PrecipMM = %v, want 0 when rain and snow are both absent", got.PrecipMM)
	}
	if got.Condition != "Clear" {
		t.Errorf("Condition = %q, want Clear", got.Condition)
	}
	if got.Icon != "01d" {
		t.Errorf("Icon = %q, want 01d", got.Icon)
	}
}

func TestOpenWeatherHistoricalSumsRainAndSnow(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/onecall/timemachine", owHistoricalPrecipJSON)
	p := newTestProvider(srv)

	got, err := p.Historical(context.Background(), 39.7392, -104.9847, historicalAt)
	if err != nil {
		t.Fatalf("Historical: %v", err)
	}
	if !almostEqual(got.PrecipMM, 0.55) {
		t.Errorf("PrecipMM = %v, want 0.55 (rain.1h + snow.1h)", got.PrecipMM)
	}
}

func TestOpenWeatherHistoricalEmptyDataArrayErrors(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/onecall/timemachine", `{"lat": 39.74, "lon": -104.98, "data": []}`)
	p := newTestProvider(srv)

	if _, err := p.Historical(context.Background(), 39.7392, -104.9847, historicalAt); err == nil {
		t.Fatal("Historical with empty data array: want error, got nil")
	}
}

// The PR #124 shape: a body carrying the readings at the root rather than
// under `data`. It exits through the same empty-data guard as the test above
// — the fixture is here because it documents the shape that actually shipped,
// and because "errors" is only half the contract. The other half is that
// nothing plausible leaks out alongside the error, since the alternative is
// storing 0 °C forever as an immutable historical fact.
func TestOpenWeatherHistoricalRootShapeErrors(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/onecall/timemachine", owHistoricalRootShapeJSON)
	p := newTestProvider(srv)

	got, err := p.Historical(context.Background(), 39.7392, -104.9847, historicalAt)
	if err == nil {
		t.Fatalf("Historical with root-shaped body: want error, got %+v", got)
	}
	if got != (Observation{}) {
		t.Errorf("Historical returned %+v alongside its error, want the zero Observation", got)
	}
}

func TestOpenWeatherHistoricalEmptyWeatherArrayDegrades(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/onecall/timemachine", owHistoricalNoWeatherJSON)
	p := newTestProvider(srv)

	got, err := p.Historical(context.Background(), 39.7392, -104.9847, historicalAt)
	if err != nil {
		t.Fatalf("Historical with empty weather array: %v", err)
	}
	if got.TempC != 18.24 {
		t.Errorf("TempC = %v, want 18.24 (temperatures must survive)", got.TempC)
	}
	if got.Condition != "" || got.Icon != "" {
		t.Errorf("Condition/Icon = %q/%q, want empty when weather array is empty", got.Condition, got.Icon)
	}
}

// 400 is the one definitive negative: the timestamp is outside the provider's
// history window and no retry will ever change that. Everything else stays
// transient, so the backfill retries it instead of recording a terminal row.
func TestOpenWeatherHistoricalBadRequestIsNoHistoricalData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   bool
	}{
		{"bad_request", http.StatusBadRequest, true},
		{"server_error", http.StatusInternalServerError, false},
		{"too_many_requests", http.StatusTooManyRequests, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `{"cod":"400","message":"requested time is out of allowed range"}`, tc.status)
			}))
			t.Cleanup(srv.Close)
			p := newTestProvider(srv)

			_, err := p.Historical(context.Background(), 39.7392, -104.9847, historicalAt)
			if err == nil {
				t.Fatalf("Historical on %d: want error, got nil", tc.status)
			}
			if got := errors.Is(err, ErrNoHistoricalData); got != tc.want {
				t.Errorf("errors.Is(err, ErrNoHistoricalData) = %v, want %v (err = %v)", got, tc.want, err)
			}
		})
	}
}

// The definitive negative is a property of the historical surface, not of the
// status code. A 400 anywhere else means we sent a malformed request, and
// reading that as "no data will ever exist" would let the backfill write
// permanent `unavailable` rows to record a bug of our own.
func TestOpenWeatherBadRequestIsTerminalOnlyForHistorical(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"cod":"400","message":"bad request"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	p := newTestProvider(srv)

	_, err := p.Current(context.Background(), 39.7392, -104.9847)
	if err == nil {
		t.Fatal("Current on 400: want error, got nil")
	}
	if errors.Is(err, ErrNoHistoricalData) {
		t.Errorf("Current 400 must not be ErrNoHistoricalData, got %v", err)
	}
}

func TestOpenWeatherGeocodeDirect(t *testing.T) {
	srv, query, path := owServer(t, "/geo/1.0/direct", owGeoDirectJSON)
	p := newTestProvider(srv)

	got, err := p.GeocodeDirect(context.Background(), "Denver", 5)
	if err != nil {
		t.Fatalf("GeocodeDirect: %v", err)
	}

	if *path != "/geo/1.0/direct" {
		t.Errorf("path = %q, want /geo/1.0/direct", *path)
	}
	q := *query
	if got := q.Get("q"); got != "Denver" {
		t.Errorf("q param = %q, want Denver", got)
	}
	if got := q.Get("limit"); got != "5" {
		t.Errorf("limit param = %q, want 5", got)
	}
	if got := q.Get("appid"); got != "test-key" {
		t.Errorf("appid param = %q, want test-key", got)
	}
	// units is a weather-data knob; geocoding calls must not carry it.
	if got := q.Get("units"); got != "" {
		t.Errorf("units param = %q, want absent on geocoding", got)
	}

	if len(got) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(got))
	}
	want := GeoResult{Name: "Denver", State: "Colorado", Country: "US", Lat: 39.7392, Lon: -104.9847}
	if got[0] != want {
		t.Errorf("results[0] = %+v, want %+v", got[0], want)
	}
	if got[1].State != "North Carolina" {
		t.Errorf("results[1].State = %q, want North Carolina", got[1].State)
	}
}

func TestOpenWeatherGeocodeReverse(t *testing.T) {
	srv, query, path := owServer(t, "/geo/1.0/reverse", owGeoReverseJSON)
	p := newTestProvider(srv)

	got, err := p.GeocodeReverse(context.Background(), 51.5073, -0.1276)
	if err != nil {
		t.Fatalf("GeocodeReverse: %v", err)
	}

	if *path != "/geo/1.0/reverse" {
		t.Errorf("path = %q, want /geo/1.0/reverse", *path)
	}
	q := *query
	if got := q.Get("lat"); got != "51.5073" {
		t.Errorf("lat param = %q, want 51.5073", got)
	}
	if got := q.Get("lon"); got != "-0.1276" {
		t.Errorf("lon param = %q, want -0.1276", got)
	}
	if got := q.Get("limit"); got != "1" {
		t.Errorf("limit param = %q, want 1", got)
	}
	if got := q.Get("appid"); got != "test-key" {
		t.Errorf("appid param = %q, want test-key", got)
	}
	// units is a weather-data knob; geocoding calls must not carry it.
	if got := q.Get("units"); got != "" {
		t.Errorf("units param = %q, want absent on geocoding", got)
	}

	if len(got) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(got))
	}
	want := GeoResult{Name: "London", Country: "GB", Lat: 51.5073, Lon: -0.1276}
	if got[0] != want {
		t.Errorf("results[0] = %+v, want %+v (State empty when provider omits it)", got[0], want)
	}
}

func TestOpenWeatherNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"cod":500,"message":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := newTestProvider(srv)
	ctx := context.Background()

	calls := []struct {
		name string
		call func() error
	}{
		{"current", func() error { _, err := p.Current(ctx, 1, 2); return err }},
		{"hourly", func() error { _, err := p.Hourly(ctx, 1, 2); return err }},
		{"daily", func() error { _, err := p.Daily(ctx, 1, 2); return err }},
		{"historical", func() error { _, err := p.Historical(ctx, 1, 2, historicalAt); return err }},
		{"geocode_direct", func() error { _, err := p.GeocodeDirect(ctx, "x", 5); return err }},
		{"geocode_reverse", func() error { _, err := p.GeocodeReverse(ctx, 1, 2); return err }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("want error on 500, got nil")
			}
			if !strings.Contains(err.Error(), "500") {
				t.Errorf("error %q should mention the status code", err)
			}
		})
	}
}

func TestOpenWeatherConfigured(t *testing.T) {
	if p := NewOpenWeatherProvider(http.DefaultClient, ""); p.Configured() {
		t.Error("Configured() with empty key = true, want false")
	}
	if p := NewOpenWeatherProvider(http.DefaultClient, "k"); !p.Configured() {
		t.Error("Configured() with key = false, want true")
	}
}
