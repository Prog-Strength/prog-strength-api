package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

// handlerCfg is svcCfg plus the handler-facing knobs.
func handlerCfg() config.WeatherConfig {
	cfg := svcCfg()
	cfg.MaxLocations = 5
	cfg.EagerLoadAllLocations = true
	return cfg
}

// handlerFixture wires the REAL Service (SQLite cache + ledger over one
// dbtest db) behind the handler, faking only the Provider — same layering as
// service_test. The user row is real too, because user_weather_locations has
// an FK to users and the unit preference is read from the users table.
type handlerFixture struct {
	router   chi.Router
	h        *Handler
	svc      *Service
	cfg      config.WeatherConfig
	provider *fakeProvider
	cache    *SQLiteCacheRepository
	locs     *SQLiteLocationsRepository
	users    *user.SQLiteRepository
	userID   string
}

func newHandlerFixture(t *testing.T, cfg config.WeatherConfig, unit user.DistanceUnit) *handlerFixture {
	t.Helper()
	db := dbtest.New(t)

	users := user.NewSQLiteRepository(db)
	u := &user.User{Email: "lifter@example.com", DisplayName: "Lifter", WeightUnit: user.WeightUnitPounds, DistanceUnit: unit}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	locs := NewSQLiteLocationsRepository(db)
	cache := NewSQLiteCacheRepository(db)
	p := newFakeProvider()
	svc := NewService(cfg, cache, NewBudgetLedger(db), p, testLogger())
	h := NewHandler(svc, locs, cfg, users, testLogger())

	r := chi.NewRouter()
	h.Mount(r)
	return &handlerFixture{
		router: r, h: h, svc: svc, cfg: cfg, provider: p,
		cache: cache, locs: locs, users: users, userID: u.ID,
	}
}

// do drives one request through the mounted router with the fixture user in
// context, mirroring the nutritionlookup handler tests.
func (f *handlerFixture) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req = req.WithContext(authctx.WithUserID(req.Context(), f.userID))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

// seed writes locations straight through the repository and returns them as
// saved (ids minted, positions assigned).
func (f *handlerFixture) seed(t *testing.T, locs ...Location) []Location {
	t.Helper()
	if err := f.locs.ReplaceAll(context.Background(), f.userID, locs); err != nil {
		t.Fatalf("seed locations: %v", err)
	}
	saved, err := f.locs.List(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("list seeded locations: %v", err)
	}
	return saved
}

func denverLocation() Location {
	state := "CO"
	return Location{Label: "Denver", Country: "US", State: &state, Lat: testLat, Lon: testLon}
}

// decodeData unmarshals the success envelope's data section into dst.
func decodeData(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	var env struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body: %s)", err, w.Body.String())
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		t.Fatalf("decode data: %v (data: %s)", err, env.Data)
	}
}

// wantError asserts an error envelope with the given status, message
// substring, and (when non-empty) machine code.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, msgContains, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, status, w.Body.String())
	}
	var env struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if !strings.Contains(env.Error, msgContains) {
		t.Errorf("error = %q, want it to contain %q", env.Error, msgContains)
	}
	if env.Code != code {
		t.Errorf("code = %q, want %q", env.Code, code)
	}
}

// readingsData is the typed GET /weather data section for assertions.
type readingsData struct {
	Status   string `json:"status"`
	Location *struct {
		ID      string  `json:"id"`
		Label   string  `json:"label"`
		State   *string `json:"state"`
		Country string  `json:"country"`
	} `json:"location"`
	FetchedAt string `json:"fetched_at"`
	Units     *struct {
		Temp string `json:"temp"`
		Wind string `json:"wind"`
	} `json:"units"`
	Current *struct {
		Temp      int    `json:"temp"`
		FeelsLike int    `json:"feels_like"`
		Humidity  int    `json:"humidity"`
		WindSpeed int    `json:"wind_speed"`
		Condition string `json:"condition"`
		Icon      string `json:"icon"`
	} `json:"current"`
	Today *struct {
		High    int       `json:"high"`
		Low     int       `json:"low"`
		Sunrise time.Time `json:"sunrise"`
		Sunset  time.Time `json:"sunset"`
	} `json:"today"`
	Hourly []struct {
		At   time.Time `json:"at"`
		Temp int       `json:"temp"`
		Icon string    `json:"icon"`
	} `json:"hourly"`
	Daily []struct {
		At           time.Time `json:"at"`
		High         int       `json:"high"`
		Low          int       `json:"low"`
		Condition    string    `json:"condition"`
		Icon         string    `json:"icon"`
		PrecipChance int       `json:"precip_chance"`
		WindSpeed    int       `json:"wind_speed"`
		Humidity     int       `json:"humidity"`
		Sunrise      time.Time `json:"sunrise"`
		Sunset       time.Time `json:"sunset"`
	} `json:"daily"`
}

type geoData struct {
	Results []GeoResult `json:"results"`
	Status  string      `json:"status"`
}

type locationsData struct {
	Locations []struct {
		ID      string  `json:"id"`
		Label   string  `json:"label"`
		State   *string `json:"state"`
		Country string  `json:"country"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
	} `json:"locations"`
	Settings *struct {
		Enabled               bool `json:"enabled"`
		MaxLocations          int  `json:"max_locations"`
		EagerLoadAllLocations bool `json:"eager_load_all_locations"`
	} `json:"settings"`
}

// ---- GET /weather ----------------------------------------------------------

func TestReadingsTimezoneValidation(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	f.seed(t, denverLocation())

	w := f.do(t, "GET", "/weather", "")
	wantError(t, w, http.StatusBadRequest, "timezone is required", "")

	w = f.do(t, "GET", "/weather?timezone=Not/AZone", "")
	wantError(t, w, http.StatusBadRequest, "invalid timezone Not/AZone", "")

	if f.provider.total() != 0 {
		t.Errorf("provider called %d times on 400s, want 0", f.provider.total())
	}
}

func TestReadingsNoSavedLocations(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	wantError(t, w, http.StatusNotFound, "no saved locations", "no_locations")
}

func TestReadingsResolvesLocation(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	boulder := Location{Label: "Boulder", Country: "US", Lat: 40.01, Lon: -105.27}
	saved := f.seed(t, denverLocation(), boulder)

	// No location_id: the first saved location wins.
	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data readingsData
	decodeData(t, w, &data)
	if data.Location == nil || data.Location.ID != saved[0].ID || data.Location.Label != "Denver" {
		t.Errorf("location = %+v, want first saved (Denver, id %s)", data.Location, saved[0].ID)
	}
	if data.Location != nil && (data.Location.State == nil || *data.Location.State != "CO") {
		t.Errorf("location.state = %v, want CO", data.Location.State)
	}

	// Explicit location_id resolves that location.
	w = f.do(t, "GET", "/weather?timezone=UTC&location_id="+saved[1].ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	decodeData(t, w, &data)
	if data.Location == nil || data.Location.ID != saved[1].ID || data.Location.Label != "Boulder" {
		t.Errorf("location = %+v, want Boulder (id %s)", data.Location, saved[1].ID)
	}

	// Unknown id is a coded 404, not a silent fallback to the first location.
	w = f.do(t, "GET", "/weather?timezone=UTC&location_id=nope", "")
	wantError(t, w, http.StatusNotFound, "location not found", "location_not_found")
}

// metricSeed pins the provider to known metric values whose conversions are
// asserted exactly: 3.3°C ⇒ 38°F, -1.7°C ⇒ 29°F, 22.5 km/h ⇒ 14 mph.
func metricSeed(f *handlerFixture) {
	f.provider.current = Current{TempC: 3.3, FeelsLikeC: -1.7, Humidity: 41, WindKMH: 22.5, Condition: "Clear", Icon: "01d"}
	sunrise := time.Now().UTC().Truncate(time.Second)
	f.provider.daily = Daily{HighC: 7.8, LowC: -4.4, Sunrise: sunrise, Sunset: sunrise.Add(14 * time.Hour)}
	f.provider.hourly = []HourlyBucket{{At: time.Now().UTC().Add(time.Hour), TempC: 3.3, Icon: "01d"}}
}

func TestReadingsMilesUnits(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	f.seed(t, denverLocation())
	metricSeed(f)

	w := f.do(t, "GET", "/weather?timezone=America/Denver", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data readingsData
	decodeData(t, w, &data)

	if data.Status != "ok" {
		t.Errorf("status = %q, want ok", data.Status)
	}
	if data.Units == nil || data.Units.Temp != "F" || data.Units.Wind != "mph" {
		t.Fatalf("units = %+v, want F/mph", data.Units)
	}
	if data.Current == nil {
		t.Fatal("current missing")
	}
	if data.Current.Temp != 38 {
		t.Errorf("current.temp = %d, want 38 (3.3C)", data.Current.Temp)
	}
	if data.Current.FeelsLike != 29 {
		t.Errorf("current.feels_like = %d, want 29 (-1.7C)", data.Current.FeelsLike)
	}
	if data.Current.WindSpeed != 14 {
		t.Errorf("current.wind_speed = %d, want 14 (22.5 km/h)", data.Current.WindSpeed)
	}
	if data.Current.Humidity != 41 {
		t.Errorf("current.humidity = %d, want 41 (unit-independent)", data.Current.Humidity)
	}
	if data.Today == nil || data.Today.High != 46 || data.Today.Low != 24 {
		t.Errorf("today = %+v, want high 46 low 24", data.Today)
	}
	if len(data.Hourly) != 1 || data.Hourly[0].Temp != 38 {
		t.Errorf("hourly = %+v, want one bucket at 38F", data.Hourly)
	}
	if data.FetchedAt == "" {
		t.Error("fetched_at missing on a fresh fetch")
	} else if _, err := time.Parse(time.RFC3339, data.FetchedAt); err != nil {
		t.Errorf("fetched_at %q is not RFC3339: %v", data.FetchedAt, err)
	}
}

func TestReadingsKilometersUnits(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitKilometers)
	f.seed(t, denverLocation())
	metricSeed(f)

	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data readingsData
	decodeData(t, w, &data)

	if data.Units == nil || data.Units.Temp != "C" || data.Units.Wind != "km/h" {
		t.Fatalf("units = %+v, want C and km/h", data.Units)
	}
	if data.Current == nil {
		t.Fatal("current missing")
	}
	if data.Current.Temp != 3 {
		t.Errorf("current.temp = %d, want 3 (3.3C rounded)", data.Current.Temp)
	}
	if data.Current.FeelsLike != -2 {
		t.Errorf("current.feels_like = %d, want -2 (-1.7C rounded)", data.Current.FeelsLike)
	}
	if data.Current.WindSpeed != 23 {
		t.Errorf("current.wind_speed = %d, want 23 (22.5 km/h rounded)", data.Current.WindSpeed)
	}
	if data.Today == nil || data.Today.High != 8 || data.Today.Low != -4 {
		t.Errorf("today = %+v, want high 8 low -4", data.Today)
	}
}

func TestReadingsHourlyKeepsEveryFutureBucket(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	f.seed(t, denverLocation())

	// Pin the handler's clock so the "future" boundary is exact, then hand the
	// provider two past buckets (must be filtered) and twelve future ones (must
	// ALL survive). The response is no longer capped at the tile strip's five:
	// the day view reads the same hours, and capping here would have made it
	// cost a second billed call for hours already in hand.
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	f.h.now = func() time.Time { return now }
	var buckets []HourlyBucket
	for i := -2; i <= 12; i++ {
		if i == 0 {
			continue
		}
		buckets = append(buckets, HourlyBucket{At: now.Add(time.Duration(i) * time.Hour), TempC: 20, Icon: "01d"})
	}
	f.provider.hourly = buckets

	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data readingsData
	decodeData(t, w, &data)
	if len(data.Hourly) != 12 {
		t.Fatalf("len(hourly) = %d, want 12 (the two past buckets filtered)", len(data.Hourly))
	}
	// Every served bucket is at >= the pinned now, in ascending order from
	// now+1h — the past is dropped, the future is not thinned.
	for i, b := range data.Hourly {
		if b.At.Before(now) {
			t.Errorf("hourly[%d].at = %v is before now %v", i, b.At, now)
		}
		if want := now.Add(time.Duration(i+1) * time.Hour); !b.At.Equal(want) {
			t.Errorf("hourly[%d].at = %v, want %v", i, b.At, want)
		}
	}
}

// TestReadingsDailyForecast pins the week view's payload: the days the daily
// call already returned, converted to the user's unit, with `pop` rendered as
// the percentage a person reads rather than the 0..1 the provider speaks.
func TestReadingsDailyForecast(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	f.seed(t, denverLocation())

	day0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	f.provider.daily = Daily{
		HighC: 28.5, LowC: 14.0,
		Sunrise: day0.Add(-6 * time.Hour), Sunset: day0.Add(8 * time.Hour),
		Days: []DayForecast{
			{
				At: day0, HighC: 28.5, LowC: 14.0, Condition: "Clear", Icon: "01d",
				PrecipChance: 0.07, WindKMH: 16.09344, Humidity: 40,
				Sunrise: day0.Add(-6 * time.Hour), Sunset: day0.Add(8 * time.Hour),
			},
			{
				At: day0.Add(24 * time.Hour), HighC: 20.0, LowC: 10.0, Condition: "Rain", Icon: "10d",
				PrecipChance: 0.5, WindKMH: 8.04672, Humidity: 70,
			},
		},
	}

	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data readingsData
	decodeData(t, w, &data)

	if len(data.Daily) != 2 {
		t.Fatalf("len(daily) = %d, want 2", len(data.Daily))
	}
	// 28.5C → 83F, 14.0C → 57F, 16.09344 km/h → 10 mph, pop 0.07 → 7%.
	first := data.Daily[0]
	if first.High != 83 || first.Low != 57 {
		t.Errorf("daily[0] high/low = %d/%d, want 83/57", first.High, first.Low)
	}
	if first.WindSpeed != 10 {
		t.Errorf("daily[0].wind_speed = %d, want 10 mph", first.WindSpeed)
	}
	if first.PrecipChance != 7 {
		t.Errorf("daily[0].precip_chance = %d, want 7 (0.07 as a percentage)", first.PrecipChance)
	}
	if first.Icon != "01d" || first.Condition != "Clear" {
		t.Errorf("daily[0] icon/condition = %q/%q, want 01d/Clear", first.Icon, first.Condition)
	}
	if !first.At.Equal(day0) {
		t.Errorf("daily[0].at = %v, want %v", first.At, day0)
	}
	// The half-percent case rounds rather than truncating: 0.5 → 50, not 49.
	if data.Daily[1].PrecipChance != 50 {
		t.Errorf("daily[1].precip_chance = %d, want 50", data.Daily[1].PrecipChance)
	}
	// Today's summary is unchanged by the arrival of the week behind it.
	if data.Today == nil || data.Today.High != 83 {
		t.Errorf("today = %+v, want the unchanged 83F high", data.Today)
	}
}

// ---- kill switch -----------------------------------------------------------

// TestDisabledRoutesStayMounted drives all five routes with Enabled=false:
// every one must answer 200 with its disabled shape — never 404, so a kill
// switch stays distinguishable from a bad deploy.
func TestDisabledRoutesStayMounted(t *testing.T) {
	cfg := handlerCfg()
	cfg.Enabled = false
	f := newHandlerFixture(t, cfg, user.DistanceUnitMiles)
	f.seed(t, denverLocation())

	// GET /weather: {"status":"disabled"} and nothing else.
	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /weather status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var raw map[string]any
	decodeData(t, w, &raw)
	if raw["status"] != "disabled" {
		t.Errorf("status = %v, want disabled", raw["status"])
	}
	if len(raw) != 1 {
		t.Errorf("disabled readings data = %v, want only {\"status\":\"disabled\"}", raw)
	}

	// GET /weather/locations: the list still serves; settings.enabled=false.
	w = f.do(t, "GET", "/weather/locations", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /weather/locations status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var locData locationsData
	decodeData(t, w, &locData)
	if locData.Settings == nil || locData.Settings.Enabled {
		t.Errorf("settings = %+v, want enabled=false", locData.Settings)
	}
	if len(locData.Locations) != 1 {
		t.Errorf("len(locations) = %d, want 1 (managing locations while disabled is fine)", len(locData.Locations))
	}

	// PUT /weather/locations: still writes.
	w = f.do(t, "PUT", "/weather/locations",
		`{"locations":[{"label":"Golden","country":"US","lat":39.75,"lon":-105.22}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /weather/locations status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	decodeData(t, w, &locData)
	if len(locData.Locations) != 1 || locData.Locations[0].Label != "Golden" {
		t.Errorf("PUT while disabled echoed %+v, want the saved Golden row", locData.Locations)
	}

	// GET /weather/search and /weather/reverse: empty results, disabled status.
	for _, target := range []string{
		"/weather/search?q=denver",
		fmt.Sprintf("/weather/reverse?lat=%v&lon=%v", testLat, testLon),
	} {
		w = f.do(t, "GET", target, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (body: %s)", target, w.Code, w.Body.String())
		}
		var geo geoData
		decodeData(t, w, &geo)
		if geo.Status != "disabled" {
			t.Errorf("%s status = %q, want disabled", target, geo.Status)
		}
		if geo.Results == nil || len(geo.Results) != 0 {
			t.Errorf("%s results = %v, want [] (not null)", target, geo.Results)
		}
	}

	if f.provider.total() != 0 {
		t.Errorf("provider called %d times while disabled, want 0", f.provider.total())
	}
}

// ---- locations -------------------------------------------------------------

func TestListLocationsEchoesSettings(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	w := f.do(t, "GET", "/weather/locations", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data locationsData
	decodeData(t, w, &data)
	if data.Locations == nil || len(data.Locations) != 0 {
		t.Errorf("locations = %v, want [] for an empty list", data.Locations)
	}
	if data.Settings == nil {
		t.Fatal("settings missing")
	}
	if !data.Settings.Enabled {
		t.Error("settings.enabled = false, want true")
	}
	if data.Settings.MaxLocations != 5 {
		t.Errorf("settings.max_locations = %d, want 5 (from cfg)", data.Settings.MaxLocations)
	}
	if !data.Settings.EagerLoadAllLocations {
		t.Error("settings.eager_load_all_locations = false, want cfg value true")
	}
}

func TestPutLocationsValidation(t *testing.T) {
	sixLocations := func() string {
		items := make([]string, 6)
		for i := range items {
			items[i] = fmt.Sprintf(`{"label":"Place %d","country":"US","lat":10,"lon":10}`, i)
		}
		return `{"locations":[` + strings.Join(items, ",") + `]}`
	}
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"too many", sixLocations(), "too many locations (max 5)"},
		{"blank label", `{"locations":[{"label":"   ","country":"US","lat":10,"lon":10}]}`, "location label is required"},
		{"label too long", `{"locations":[{"label":"` + strings.Repeat("a", 101) + `","country":"US","lat":10,"lon":10}]}`,
			"location label is too long (max 100 characters)"},
		{"missing country", `{"locations":[{"label":"X","lat":10,"lon":10}]}`, "location country is required"},
		{"country too long", `{"locations":[{"label":"X","country":"` + strings.Repeat("c", 11) + `","lat":10,"lon":10}]}`,
			"location country is too long (max 10 characters)"},
		{"state too long", `{"locations":[{"label":"X","state":"` + strings.Repeat("s", 11) + `","country":"US","lat":10,"lon":10}]}`,
			"location state is too long (max 10 characters)"},
		{"lat too big", `{"locations":[{"label":"X","country":"US","lat":90.5,"lon":10}]}`, "invalid coordinates"},
		{"lon too small", `{"locations":[{"label":"X","country":"US","lat":10,"lon":-180.5}]}`, "invalid coordinates"},
		{"malformed body", `{"locations":`, "invalid request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
			existing := f.seed(t, denverLocation())
			w := f.do(t, "PUT", "/weather/locations", tt.body)
			wantError(t, w, http.StatusBadRequest, tt.wantMsg, "")

			// A rejected PUT must not have touched the stored list.
			after, err := f.locs.List(context.Background(), f.userID)
			if err != nil {
				t.Fatalf("list after rejected PUT: %v", err)
			}
			if len(after) != 1 || after[0].ID != existing[0].ID {
				t.Errorf("stored list changed after a 400: %+v", after)
			}
		})
	}
}

// TestLocationsRoundTrip is the read-modify-write hazard guard: GET the list,
// reorder it client-side, PUT it back, and assert ids and coordinates survive
// unchanged. This is exactly why GET includes lat/lon — a reorder must not be
// able to destroy them.
func TestLocationsRoundTrip(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)

	// Create two locations through the API (no ids: server mints them).
	w := f.do(t, "PUT", "/weather/locations", `{"locations":[
		{"label":"Denver","state":"CO","country":"US","lat":39.7392,"lon":-104.9903},
		{"label":"Boulder","country":"US","lat":40.01,"lon":-105.27}
	]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("initial PUT status = %d (body: %s)", w.Code, w.Body.String())
	}

	w = f.do(t, "GET", "/weather/locations", "")
	var got locationsData
	decodeData(t, w, &got)
	if len(got.Locations) != 2 {
		t.Fatalf("len(locations) = %d, want 2", len(got.Locations))
	}
	denver, boulder := got.Locations[0], got.Locations[1]
	if denver.ID == "" || boulder.ID == "" {
		t.Fatal("saved locations missing ids")
	}
	if denver.State == nil || *denver.State != "CO" {
		t.Errorf("denver.state = %v, want CO", denver.State)
	}

	// Reorder client-side: Boulder first. Echo every field GET returned.
	reordered, err := json.Marshal(map[string]any{"locations": []any{boulder, denver}})
	if err != nil {
		t.Fatalf("marshal reordered list: %v", err)
	}
	w = f.do(t, "PUT", "/weather/locations", string(reordered))
	if w.Code != http.StatusOK {
		t.Fatalf("reorder PUT status = %d (body: %s)", w.Code, w.Body.String())
	}

	w = f.do(t, "GET", "/weather/locations", "")
	decodeData(t, w, &got)
	if len(got.Locations) != 2 {
		t.Fatalf("len(locations) after reorder = %d, want 2", len(got.Locations))
	}
	first, second := got.Locations[0], got.Locations[1]
	if first.ID != boulder.ID || second.ID != denver.ID {
		t.Errorf("reorder changed ids: got (%s, %s), want (%s, %s)",
			first.ID, second.ID, boulder.ID, denver.ID)
	}
	if first.Label != "Boulder" || first.Lat != 40.01 || first.Lon != -105.27 {
		t.Errorf("boulder after round-trip = %+v, want coordinates intact", first)
	}
	if second.Label != "Denver" || second.Lat != 39.7392 || second.Lon != -104.9903 {
		t.Errorf("denver after round-trip = %+v, want coordinates intact", second)
	}
	if second.State == nil || *second.State != "CO" {
		t.Errorf("denver.state after round-trip = %v, want CO", second.State)
	}
}

// ---- search / reverse ------------------------------------------------------

func TestSearchValidation(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantMsg string
	}{
		{"missing q", "/weather/search", "q is required"},
		{"blank q", "/weather/search?q=%20%20", "q is required"},
		{"q too long", "/weather/search?q=" + strings.Repeat("a", 201), "q is too long"},
		{"limit zero", "/weather/search?q=denver&limit=0", "limit must be an integer between 1 and 5"},
		{"limit too big", "/weather/search?q=denver&limit=6", "limit must be an integer between 1 and 5"},
		{"limit junk", "/weather/search?q=denver&limit=lots", "limit must be an integer between 1 and 5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
			w := f.do(t, "GET", tt.target, "")
			wantError(t, w, http.StatusBadRequest, tt.wantMsg, "")
			if f.provider.total() != 0 {
				t.Errorf("provider called %d times on a 400, want 0", f.provider.total())
			}
		})
	}
}

func TestSearchSuccessShape(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	w := f.do(t, "GET", "/weather/search?q=denver", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var geo geoData
	decodeData(t, w, &geo)
	if geo.Status != "ok" {
		t.Errorf("status = %q, want ok", geo.Status)
	}
	if len(geo.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(geo.Results))
	}
	r := geo.Results[0]
	if r.Name != "Denver" || r.State != "Colorado" || r.Country != "US" || r.Lat != testLat || r.Lon != testLon {
		t.Errorf("result = %+v", r)
	}
}

func TestReverseValidation(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{"missing both", "/weather/reverse"},
		{"missing lon", fmt.Sprintf("/weather/reverse?lat=%v", testLat)},
		{"lat out of range", "/weather/reverse?lat=91&lon=10"},
		{"lon out of range", "/weather/reverse?lat=10&lon=181"},
		{"junk", "/weather/reverse?lat=north&lon=west"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
			w := f.do(t, "GET", tt.target, "")
			wantError(t, w, http.StatusBadRequest, "invalid coordinates", "")
			if f.provider.total() != 0 {
				t.Errorf("provider called %d times on a 400, want 0", f.provider.total())
			}
		})
	}
}

func TestReverseSuccessShape(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	w := f.do(t, "GET", fmt.Sprintf("/weather/reverse?lat=%v&lon=%v", testLat, testLon), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var geo geoData
	decodeData(t, w, &geo)
	if geo.Status != "ok" {
		t.Errorf("status = %q, want ok", geo.Status)
	}
	if len(geo.Results) != 1 || geo.Results[0].Name != "Denver" {
		t.Errorf("results = %+v, want the Denver candidate", geo.Results)
	}
}

// TestMissingUserInContextIs500 pins the auth precondition on a
// representative route, matching the nutritionlookup precedent.
func TestMissingUserInContextIs500(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	req := httptest.NewRequest("GET", "/weather?timezone=UTC", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when auth middleware was not applied", w.Code)
	}
}

// ---- id validation on PUT --------------------------------------------------

// TestPutLocationsRejectsDuplicateID: the id column is a global primary key,
// so the same id twice in one request would be a PK conflict mid-transaction.
// It must surface as a 400, and the stored list must be untouched.
func TestPutLocationsRejectsDuplicateID(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	saved := f.seed(t, denverLocation())

	body := fmt.Sprintf(`{"locations":[
		{"id":%q,"label":"A","country":"US","lat":1,"lon":1},
		{"id":%q,"label":"B","country":"US","lat":2,"lon":2}
	]}`, saved[0].ID, saved[0].ID)
	w := f.do(t, "PUT", "/weather/locations", body)
	wantError(t, w, http.StatusBadRequest, "invalid location id", "")

	after, err := f.locs.List(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("list after rejected PUT: %v", err)
	}
	if len(after) != 1 || after[0].ID != saved[0].ID || after[0].Label != "Denver" {
		t.Errorf("stored list changed after a 400: %+v", after)
	}
}

// TestPutLocationsRejectsForeignID: an id belonging to another user's row
// would also collide on the global primary key (the replace only deletes the
// caller's rows). A 400, and the other user's row stays intact.
func TestPutLocationsRejectsForeignID(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)

	other := &user.User{Email: "other@example.com", DisplayName: "Other", WeightUnit: user.WeightUnitPounds, DistanceUnit: user.DistanceUnitMiles}
	if err := f.users.Create(context.Background(), other); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	if err := f.locs.ReplaceAll(context.Background(), other.ID, []Location{
		{Label: "Golden", Country: "US", Lat: 39.75, Lon: -105.22},
	}); err != nil {
		t.Fatalf("seed other user's location: %v", err)
	}
	theirs, err := f.locs.List(context.Background(), other.ID)
	if err != nil {
		t.Fatalf("list other user's locations: %v", err)
	}

	body := fmt.Sprintf(`{"locations":[{"id":%q,"label":"Hijack","country":"US","lat":10,"lon":10}]}`, theirs[0].ID)
	w := f.do(t, "PUT", "/weather/locations", body)
	wantError(t, w, http.StatusBadRequest, "invalid location id", "")

	// The caller saved nothing and the other user's row is untouched.
	mine, err := f.locs.List(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("list caller's locations: %v", err)
	}
	if len(mine) != 0 {
		t.Errorf("caller's list = %+v, want empty after the rejected PUT", mine)
	}
	after, err := f.locs.List(context.Background(), other.ID)
	if err != nil {
		t.Fatalf("re-list other user's locations: %v", err)
	}
	if len(after) != 1 || after[0].ID != theirs[0].ID || after[0].Label != "Golden" {
		t.Errorf("other user's row changed: %+v", after)
	}
}

func TestPutLocationsNormalizesBlankStateToNull(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitMiles)
	w := f.do(t, "PUT", "/weather/locations",
		`{"locations":[{"label":"Denver","state":"  ","country":"US","lat":39.74,"lon":-104.99}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data locationsData
	decodeData(t, w, &data)
	if len(data.Locations) != 1 {
		t.Fatalf("len(locations) = %d, want 1", len(data.Locations))
	}
	if data.Locations[0].State != nil {
		t.Errorf("state = %q, want omitted (blank state stored as NULL)", *data.Locations[0].State)
	}
}

// ---- degraded readings pass-through ----------------------------------------

// TestReadingsBudgetExhaustedPassthrough: a refused reservation with stale
// cache serves the stale sections under status budget_exhausted — the handler
// passes the service status through verbatim with location/units attached and
// the omitempty sections populated from the stale rows.
func TestReadingsBudgetExhaustedPassthrough(t *testing.T) {
	cfg := handlerCfg()
	cfg.DailyCallBudget = 0 // ceiling 0: every reservation is refused
	f := newHandlerFixture(t, cfg, user.DistanceUnitMiles)
	loc := f.seed(t, denverLocation())[0]

	// Stale rows for all three endpoints, fetched a day ago (past every TTL).
	old := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	seedReading(t, f.cache, ReadingKey(testLat, testLon, EndpointCurrent),
		Current{TempC: 3.3, FeelsLikeC: -1.7, Humidity: 41, WindKMH: 22.5, Condition: "Clear", Icon: "01d"}, old)
	seedReading(t, f.cache, ReadingKey(testLat, testLon, EndpointHourly),
		[]HourlyBucket{{At: time.Now().UTC().Add(time.Hour), TempC: 3.3, Icon: "01d"}}, old)
	seedReading(t, f.cache, ReadingKey(testLat, testLon, EndpointDaily),
		Daily{HighC: 7.8, LowC: -4.4, Sunrise: old, Sunset: old.Add(14 * time.Hour)}, old)

	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var data readingsData
	decodeData(t, w, &data)

	if data.Status != "budget_exhausted" {
		t.Errorf("status = %q, want budget_exhausted", data.Status)
	}
	if data.Location == nil || data.Location.ID != loc.ID {
		t.Errorf("location = %+v, want the resolved saved location", data.Location)
	}
	if data.Units == nil || data.Units.Temp != "F" || data.Units.Wind != "mph" {
		t.Errorf("units = %+v, want F/mph even on a degraded payload", data.Units)
	}
	if data.Current == nil || data.Current.Temp != 38 {
		t.Errorf("current = %+v, want the stale row converted (38F)", data.Current)
	}
	if data.Today == nil || data.Today.High != 46 {
		t.Errorf("today = %+v, want the stale daily attached (high 46)", data.Today)
	}
	if len(data.Hourly) != 1 {
		t.Errorf("hourly = %+v, want the stale future bucket attached", data.Hourly)
	}
	if want := old.Format(time.RFC3339); data.FetchedAt != want {
		t.Errorf("fetched_at = %q, want the stale rows' fetch time %q", data.FetchedAt, want)
	}
	if f.provider.total() != 0 {
		t.Errorf("provider called %d times with the budget refused, want 0", f.provider.total())
	}
}

// ---- unit-preference fallback ----------------------------------------------

// failingUserReader always errors, standing in for a flaky users table.
type failingUserReader struct{}

func (failingUserReader) GetByID(ctx context.Context, id string) (*user.User, error) {
	return nil, errors.New("user store down")
}

// TestReadingsUnitFallbackWhenUserReadFails: a failed preference read must
// degrade to the "mi" default (the users-table default), never 500 the whole
// weather request.
func TestReadingsUnitFallbackWhenUserReadFails(t *testing.T) {
	f := newHandlerFixture(t, handlerCfg(), user.DistanceUnitKilometers)
	f.seed(t, denverLocation())
	metricSeed(f)

	// Rewire the handler with a users seam that always fails; everything
	// else (service, locations, cfg) stays real.
	h := NewHandler(f.svc, f.locs, f.cfg, failingUserReader{}, testLogger())
	r := chi.NewRouter()
	h.Mount(r)
	f.router = r

	w := f.do(t, "GET", "/weather?timezone=UTC", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a preference read failure must not fail the request; body: %s)", w.Code, w.Body.String())
	}
	var data readingsData
	decodeData(t, w, &data)
	if data.Units == nil || data.Units.Temp != "F" || data.Units.Wind != "mph" {
		t.Errorf("units = %+v, want the F/mph default when the user read fails", data.Units)
	}
	if data.Current == nil || data.Current.Temp != 38 {
		t.Errorf("current = %+v, want the mi-converted reading (38F)", data.Current)
	}
}
