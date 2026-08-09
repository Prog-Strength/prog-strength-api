package activity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth/authctx"
	"github.com/Prog-Strength/prog-strength-api/internal/weather"
)

// waitTimeout bounds every wait in this file. Long enough that a slow CI box
// never flakes, short enough that a broken wiring fails the run instead of
// hanging it.
const waitTimeout = 5 * time.Second

// spawnWindow is how long a NEGATIVE assertion waits before concluding no
// capture goroutine was started. A guard that let one through would have
// scheduled it during the upload's own SQLite writes, long before this
// elapses.
const spawnWindow = 250 * time.Millisecond

// weatherCall is one recorded Capture, including the properties of the
// context it was handed — which is the whole point of the detached test.
type weatherCall struct {
	activityID  string
	lat, lon    float64
	at          time.Time
	ctxErr      error
	deadline    time.Time
	hasDeadline bool
}

// fakeWeatherCapturer records Capture calls. It answers Get with "no row" and
// nothing else: the detail-DTO tests read through the real repository instead
// (see storedWeather), because a Get that ignores its activity id cannot fail
// when the handler looks up the wrong one.
type fakeWeatherCapturer struct {
	mu    sync.Mutex
	calls []weatherCall

	// err is returned by every Capture.
	err error

	// release, when non-nil, holds Capture until the test closes it. It is
	// what lets the detached test sample ctx.Err() strictly AFTER the request
	// context has been cancelled.
	release chan struct{}

	// done carries each recorded call. Buffered and sent to non-blockingly so
	// a capture never blocks on a test that is not listening.
	done chan weatherCall
}

func newFakeWeatherCapturer() *fakeWeatherCapturer {
	return &fakeWeatherCapturer{done: make(chan weatherCall, 4)}
}

func (f *fakeWeatherCapturer) Capture(ctx context.Context, activityID string, lat, lon float64, at time.Time) error {
	if f.release != nil {
		select {
		case <-f.release:
		case <-time.After(waitTimeout):
		}
	}
	c := weatherCall{activityID: activityID, lat: lat, lon: lon, at: at, ctxErr: ctx.Err()}
	c.deadline, c.hasDeadline = ctx.Deadline()
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	select {
	case f.done <- c:
	default:
	}
	return f.err
}

func (f *fakeWeatherCapturer) Get(context.Context, string) (*weather.ActivityWeather, error) {
	return nil, nil
}

// awaitCall blocks until one capture lands, or fails the test.
func (f *fakeWeatherCapturer) awaitCall(t *testing.T) weatherCall {
	t.Helper()
	select {
	case c := <-f.done:
		return c
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the detached weather capture")
		return weatherCall{}
	}
}

// expectNoCall fails if a capture is spawned within the spawn window.
func (f *fakeWeatherCapturer) expectNoCall(t *testing.T, why string) {
	t.Helper()
	select {
	case c := <-f.done:
		t.Fatalf("weather capture ran for %s (activity %s)", why, c.activityID)
	case <-time.After(spawnWindow):
	}
}

func (f *fakeWeatherCapturer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newWeatherHandler is newTestHandler plus a wired fake capturer.
func newWeatherHandler(t *testing.T) (*Handler, *SQLiteRepository, *fakeWeatherCapturer) {
	t.Helper()
	h, _, repo := newTestHandler(t)
	h.SetRegistry(newTestRegistry(t, repo))
	fake := newFakeWeatherCapturer()
	h.SetWeatherCapturer(fake)
	return h, repo, fake
}

// storedWeather backs the seam's read with the REAL
// weather.SQLiteActivityWeatherRepository, over the same database the handler
// already reads from. The detail-DTO tests use it so "the DTO carries this
// row" is a statement about a row on disk keyed by activity id, rather than
// about a fake that answers every id with the same struct.
//
// Capture only ever runs as the detached spawn from importing a fixture, and
// failing is the right answer there: the import path swallows it and writes no
// row, so the DTO under test still describes exactly what the test stored.
type storedWeather struct {
	repo *weather.SQLiteActivityWeatherRepository
}

func (s storedWeather) Capture(context.Context, string, float64, float64, time.Time) error {
	return errors.New("the detail-DTO tests capture nothing")
}

func (s storedWeather) Get(ctx context.Context, activityID string) (*weather.ActivityWeather, error) {
	return s.repo.Get(ctx, activityID)
}

// newStoredWeatherHandler is newWeatherHandler with durable rows instead of a
// canned one.
func newStoredWeatherHandler(t *testing.T) (*Handler, *SQLiteRepository, *weather.SQLiteActivityWeatherRepository) {
	t.Helper()
	h, _, repo := newTestHandler(t)
	h.SetRegistry(newTestRegistry(t, repo))
	weatherRepo := weather.NewSQLiteActivityWeatherRepository(repo.db)
	h.SetWeatherCapturer(storedWeather{repo: weatherRepo})
	return h, repo, weatherRepo
}

// putWeather stores one row through the real repository.
func putWeather(t *testing.T, repo *weather.SQLiteActivityWeatherRepository, row *weather.ActivityWeather) {
	t.Helper()
	if err := repo.Put(context.Background(), *row); err != nil {
		t.Fatalf("store activity_weather row for %q: %v", row.ActivityID, err)
	}
}

// countActivityWeather is the "no row was written" assertion the SOW's
// non-fatal contract rests on: a failed capture must leave the activity
// indistinguishable from one never attempted, so the backfill stays the single
// repair path.
func countActivityWeather(t *testing.T, repo *SQLiteRepository) int {
	t.Helper()
	var n int
	if err := repo.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM activity_weather`).Scan(&n); err != nil {
		t.Fatalf("count activity_weather: %v", err)
	}
	return n
}

// --- Task 8: the detached capture ----------------------------------

// TestUploadTCX_CaptureRunsOnADetachedContext is the highest-value test here.
// It drives a REAL http server, because only net/http cancels the request
// context when the handler returns — and that cancellation is the thing being
// guarded against. The fake holds the capture until after the server has shut
// the request down, so wiring captureWeather to r.Context() makes the recorded
// ctx.Err() a cancellation and fails this test deterministically.
func TestUploadTCX_CaptureRunsOnADetachedContext(t *testing.T) {
	h, _, fake := newWeatherHandler(t)
	fake.release = make(chan struct{})

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(authctx.WithUserID(req.Context(), testUserID)))
		})
	})
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	body, ct := multipartBody(t, readFixture(t, "typical_5k.tcx"))
	resp, err := srv.Client().Post(srv.URL+"/activities/tcx", ct, body)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read upload response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var env activityEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}

	// Close waits for the connection to finish, which is strictly after
	// net/http has cancelled the request context. Only then is the capture let
	// go, so the sample below cannot race the cancellation.
	srv.Close()
	close(fake.release)

	call := fake.awaitCall(t)
	if call.ctxErr != nil {
		t.Fatalf("capture context error = %v, want nil: the capture inherited the request context and was cancelled with it", call.ctxErr)
	}
	if !call.hasDeadline {
		t.Fatal("capture context carried no deadline: a hung provider would leak a goroutine per upload")
	}
	// Still in the future (so it was not inherited from something already
	// cancelled) and no further out than the constant allows, with enough slack
	// for the upload itself.
	if remaining := time.Until(call.deadline); remaining <= 0 || remaining > weatherCaptureTimeout || remaining < weatherCaptureTimeout-10*time.Second {
		t.Fatalf("capture deadline is %v away, want close to %v", remaining, weatherCaptureTimeout)
	}
	if call.activityID != env.Data.ID {
		t.Errorf("captured activity %q, want %q", call.activityID, env.Data.ID)
	}
	if call.lat != 40.0 || call.lon != -105.0 {
		t.Errorf("captured coordinate = (%v, %v), want the fixture's first position (40, -105)", call.lat, call.lon)
	}
	if !call.at.Equal(env.Data.StartTime) {
		t.Errorf("captured at = %v, want the activity start %v", call.at, env.Data.StartTime)
	}
}

// A weather failure must never reach the uploader, and must never leave a row
// behind — the missing row is what keeps cmd/weather-backfill the single
// repair path.
func TestUploadTCX_CaptureFailureIsNonFatal(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"generic provider error", errors.New("openweather: 503")},
		{"timeout", context.DeadlineExceeded},
		{"budget exhausted", weather.ErrBudgetExhausted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, repo, fake := newWeatherHandler(t)
			fake.err = tc.err

			w := doImport(t, h, readFixture(t, "typical_5k.tcx"))
			if w.Code != http.StatusCreated {
				t.Fatalf("upload status = %d, want 201 despite %v; body=%s", w.Code, tc.err, w.Body.String())
			}
			// Wait for the capture to actually fail before asserting on the
			// table, so this is a statement about the outcome and not about
			// scheduling.
			fake.awaitCall(t)
			if n := countActivityWeather(t, repo); n != 0 {
				t.Errorf("activity_weather has %d rows after a failed capture, want 0", n)
			}
		})
	}
}

// An indoor activity has no conditions worth recording and must not spend a
// provider call.
func TestUploadTCX_IndoorSpawnsNoCapture(t *testing.T) {
	h, _, fake := newWeatherHandler(t)

	w := doImport(t, h, readFixture(t, "treadmill_5k.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	fake.expectNoCall(t, "an indoor activity")
	if n := fake.callCount(); n != 0 {
		t.Errorf("capture calls = %d, want 0", n)
	}
}

// An outdoor activity with no positioned trackpoint has no coordinate to ask
// about. The terminal no_coordinates row is the backfill's to write, so the
// import path spawns nothing at all. (Only a retag can produce this shape, so
// the guard is driven directly rather than through an upload.)
func TestCaptureWeather_NoPositionSpawnsNothing(t *testing.T) {
	h, _, fake := newWeatherHandler(t)

	h.captureWeather(Activity{
		ID:          "a1",
		Environment: EnvironmentOutdoor,
		StartTime:   time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Trackpoints: []Trackpoint{{Sequence: 0}, {Sequence: 1}},
	})

	fake.expectNoCall(t, "an outdoor activity with no position")
}

// TestCaptureWeather_IndoorWithPositionSpawnsNothing is what actually pins the
// environment guard. The treadmill FIXTURE carries no positions at all, so
// TestUploadTCX_IndoorSpawnsNoCapture would still pass with the environment
// check deleted — firstPosition short-circuits first. An indoor activity WITH a
// GPS track is a real shape (a watch that kept its fix indoors, or an outdoor
// activity retagged indoor and re-imported), and only the environment check
// stops it spending a call.
func TestCaptureWeather_IndoorWithPositionSpawnsNothing(t *testing.T) {
	h, _, fake := newWeatherHandler(t)
	lat, lon := 40.0, -105.0

	h.captureWeather(Activity{
		ID:          "a1",
		Environment: EnvironmentIndoor,
		StartTime:   time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Trackpoints: []Trackpoint{{Sequence: 0, Latitude: &lat, Longitude: &lon}},
	})

	fake.expectNoCall(t, "an indoor activity with a positioned track")
	if n := fake.callCount(); n != 0 {
		t.Errorf("capture calls = %d, want 0", n)
	}
}

// A handler with no capturer wired (every existing test, and any deployment
// with weather off) must behave exactly as before.
func TestUploadTCX_NilWeatherCapturerIsANoOp(t *testing.T) {
	h, _, _ := newTestHandler(t)

	w := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d with no capturer wired; body=%s", w.Code, w.Body.String())
	}
}

func TestFirstPosition(t *testing.T) {
	lat, lon := 40.5, -105.5
	cases := []struct {
		name     string
		tps      []Trackpoint
		wantOK   bool
		wantLat  float64
		wantLon  float64
		wantDesc string
	}{
		{name: "no trackpoints"},
		{name: "no positions", tps: []Trackpoint{{}, {}}},
		{
			name:    "first positioned point wins",
			tps:     []Trackpoint{{}, {Latitude: &lat, Longitude: &lon}, {}},
			wantOK:  true,
			wantLat: lat, wantLon: lon,
		},
		{
			// A half-populated point is not a position: one coordinate alone
			// would place the activity on the equator or the prime meridian.
			name: "latitude without longitude",
			tps:  []Trackpoint{{Latitude: &lat}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotLat, gotLon, ok := firstPosition(tc.tps)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (gotLat != tc.wantLat || gotLon != tc.wantLon) {
				t.Fatalf("position = (%v, %v), want (%v, %v)", gotLat, gotLon, tc.wantLat, tc.wantLon)
			}
		})
	}
}

// --- Task 9: the DTO field -----------------------------------------

// weatherKey pulls the raw weather object off a detail response so absent
// keys stay distinguishable from present-but-null ones.
func weatherKey(t *testing.T, w *httptest.ResponseRecorder) (map[string]any, bool) {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode detail: %v; body=%s", err, w.Body.String())
	}
	raw, ok := env.Data["weather"]
	if !ok {
		return nil, false
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("weather = %#v, want an object", raw)
	}
	return obj, true
}

func okWeatherRow(activityID string) *weather.ActivityWeather {
	lat, lon := 40.0, -105.0
	return &weather.ActivityWeather{
		ActivityID: activityID,
		Status:     weather.ActivityStatusOK,
		Lat:        &lat,
		Lon:        &lon,
		Observation: &weather.Observation{
			ObservedAt: time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC),
			TempC:      31.1,
			FeelsLikeC: 36.4,
			DewPointC:  21.7,
			Humidity:   58,
			WindKMH:    14.5,
			WindDeg:    210,
			// Zero precipitation on an ok row still serializes: the client
			// hides the cell, the API does not pretend it never measured.
			PrecipMM:  0,
			Condition: "Clear",
			Icon:      "01d",
		},
		FetchedAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
	}
}

func TestDetail_WeatherKeyAbsentWithNoRow(t *testing.T) {
	t.Run("capturer wired, no row", func(t *testing.T) {
		h, _, _ := newStoredWeatherHandler(t)
		id := importedID(t, h, "typical_5k.tcx")

		if _, present := weatherKey(t, doGet(t, h, id, "")); present {
			t.Error("weather key present for an activity with no row, want absent")
		}
	})
	t.Run("capturer unwired", func(t *testing.T) {
		h, _, repo := newTestHandler(t)
		h.SetRegistry(newTestRegistry(t, repo))
		id := importedID(t, h, "typical_5k.tcx")

		if _, present := weatherKey(t, doGet(t, h, id, "")); present {
			t.Error("weather key present with no capturer wired, want absent")
		}
	})
}

func TestDetail_OKRowSerializesEveryField(t *testing.T) {
	h, _, weatherRepo := newStoredWeatherHandler(t)
	id := importedID(t, h, "typical_5k.tcx")
	putWeather(t, weatherRepo, okWeatherRow(id))
	// A second activity carrying a DIFFERENT row, so "the response borrowed
	// someone else's reading" fails on the temperature rather than passing
	// because every fixture happens to be identical.
	decoy := okWeatherRow(importedID(t, h, "treadmill_5k.tcx"))
	decoy.Observation.TempC = -40
	putWeather(t, weatherRepo, decoy)

	got, present := weatherKey(t, doGet(t, h, id, ""))
	if !present {
		t.Fatal("weather key absent for an ok row")
	}
	want := map[string]any{
		"status":       "ok",
		"observed_at":  "2026-01-02T08:00:00Z",
		"temp_c":       31.1,
		"feels_like_c": 36.4,
		"dew_point_c":  21.7,
		"humidity":     float64(58),
		"wind_kmh":     14.5,
		"wind_deg":     float64(210),
		"precip_mm":    float64(0),
		"condition":    "Clear",
		"icon":         "01d",
	}
	for k, wantV := range want {
		gotV, ok := got[k]
		if !ok {
			t.Errorf("weather.%s absent, want %v", k, wantV)
			continue
		}
		if gotV != wantV {
			t.Errorf("weather.%s = %#v, want %#v", k, gotV, wantV)
		}
	}
	if len(got) != len(want) {
		t.Errorf("weather has %d keys (%v), want exactly %d", len(got), got, len(want))
	}
}

func TestDetail_NonOKRowDropsReadingFields(t *testing.T) {
	h, _, weatherRepo := newStoredWeatherHandler(t)
	id := importedID(t, h, "typical_5k.tcx")
	putWeather(t, weatherRepo, &weather.ActivityWeather{
		ActivityID: id,
		Status:     weather.ActivityStatusNoCoordinates,
		FetchedAt:  time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
	})

	got, present := weatherKey(t, doGet(t, h, id, ""))
	if !present {
		t.Fatal("weather key absent for a no_coordinates row")
	}
	if len(got) != 1 || got["status"] != string(weather.ActivityStatusNoCoordinates) {
		t.Fatalf("weather = %v, want only {status: no_coordinates}", got)
	}
}

// Retagging outdoor -> indoor keeps the stored reading on the wire. Hiding the
// widget is the client's job; dropping the row here would turn a reversible UI
// toggle into a budget-spending operation.
func TestDetail_WeatherSurvivesAnIndoorRetag(t *testing.T) {
	h, repo, weatherRepo := newStoredWeatherHandler(t)
	id := importedID(t, h, "typical_5k.tcx")
	putWeather(t, weatherRepo, okWeatherRow(id))

	if w := doPatch(t, h, id, `{"environment":"indoor"}`); w.Code != http.StatusOK {
		t.Fatalf("retag status = %d; body=%s", w.Code, w.Body.String())
	}

	// The row is still on disk. The retag rebuilds best efforts and trackpoints
	// for the activity, so "survives" has to mean the storage survived, not
	// just that a read path was willing to serve something.
	if n := countActivityWeather(t, repo); n != 1 {
		t.Fatalf("activity_weather has %d rows after an outdoor -> indoor retag, want 1", n)
	}

	got, present := weatherKey(t, doGet(t, h, id, ""))
	if !present {
		t.Fatal("weather key absent after an outdoor -> indoor retag, want the row retained")
	}
	if got["status"] != "ok" || got["temp_c"] != 31.1 {
		t.Fatalf("weather = %v, want the stored ok reading unchanged", got)
	}
}
