package weather

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// These canned bodies ARE the One Call 4.0 parse contract: the provider is
// pinned against them, not against live OpenWeather responses.

const owCurrentJSON = `{
	"lat": 39.74, "lon": -104.99, "timezone": "America/Denver",
	"dt": 1754740800,
	"temp": 21.4,
	"feels_like": 20.1,
	"humidity": 45,
	"wind_speed": 2.5,
	"weather": [{"id": 800, "main": "Clear", "description": "clear sky", "icon": "01d"}]
}`

// owHourlyJSON carries 14 buckets so the 12-bucket truncation is observable.
func owHourlyJSON() string {
	var entries []string
	for i := 0; i < 14; i++ {
		entries = append(entries, fmt.Sprintf(
			`{"dt": %d, "temp": %g, "weather": [{"main": "Clouds", "icon": "0%dd"}]}`,
			1754740800+int64(i)*3600, 20.5+float64(i), (i%9)+1))
	}
	return `{"data": [` + strings.Join(entries, ",") + `]}`
}

const owDailyJSON = `{
	"data": [{
		"dt": 1754740800,
		"sunrise": 1754727120, "sunset": 1754777580,
		"temp": {"max": 31.2, "min": 16.8, "day": 28.0},
		"weather": [{"main": "Clear", "icon": "01d"}]
	}]
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
	srv, query, path := owServer(t, "/data/4.0/current", owCurrentJSON)
	p := newTestProvider(srv)

	got, err := p.Current(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	if *path != "/data/4.0/current" {
		t.Errorf("path = %q, want /data/4.0/current", *path)
	}
	assertCommonParams(t, *query, "39.7392", "-104.9847")

	if got.TempC != 21.4 {
		t.Errorf("TempC = %v, want 21.4", got.TempC)
	}
	if got.FeelsLikeC != 20.1 {
		t.Errorf("FeelsLikeC = %v, want 20.1", got.FeelsLikeC)
	}
	if got.Humidity != 45 {
		t.Errorf("Humidity = %v, want 45", got.Humidity)
	}
	// OpenWeather metric wind is m/s; normalized model is km/h (2.5 * 3.6).
	if !almostEqual(got.WindKMH, 9.0) {
		t.Errorf("WindKMH = %v, want 9.0", got.WindKMH)
	}
	if got.Condition != "Clear" {
		t.Errorf("Condition = %q, want Clear", got.Condition)
	}
	if got.Icon != "01d" {
		t.Errorf("Icon = %q, want 01d", got.Icon)
	}
}

func TestOpenWeatherHourlyTruncatesToTwelveBuckets(t *testing.T) {
	srv, query, path := owServer(t, "/data/4.0/timeline/1h", owHourlyJSON())
	p := newTestProvider(srv)

	got, err := p.Hourly(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Hourly: %v", err)
	}

	if *path != "/data/4.0/timeline/1h" {
		t.Errorf("path = %q, want /data/4.0/timeline/1h", *path)
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
	srv, query, path := owServer(t, "/data/4.0/timeline/1day", owDailyJSON)
	p := newTestProvider(srv)

	got, err := p.Daily(context.Background(), 39.7392, -104.9847)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}

	if *path != "/data/4.0/timeline/1day" {
		t.Errorf("path = %q, want /data/4.0/timeline/1day", *path)
	}
	assertCommonParams(t, *query, "39.7392", "-104.9847")

	if got.HighC != 31.2 {
		t.Errorf("HighC = %v, want 31.2", got.HighC)
	}
	if got.LowC != 16.8 {
		t.Errorf("LowC = %v, want 16.8", got.LowC)
	}
	if want := time.Unix(1754727120, 0).UTC(); !got.Sunrise.Equal(want) {
		t.Errorf("Sunrise = %v, want %v", got.Sunrise, want)
	}
	if want := time.Unix(1754777580, 0).UTC(); !got.Sunset.Equal(want) {
		t.Errorf("Sunset = %v, want %v", got.Sunset, want)
	}
}

func TestOpenWeatherDailyEmptyTimelineErrors(t *testing.T) {
	srv, _, _ := owServer(t, "/data/4.0/timeline/1day", `{"data": []}`)
	p := newTestProvider(srv)

	if _, err := p.Daily(context.Background(), 39.7, -104.9); err == nil {
		t.Fatal("Daily with empty timeline: want error, got nil")
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
