package activity

// The import-time conditions capture: WHEN a reading is taken, and nothing
// else. The budget, the provider call, and the durable store all live in
// internal/weather (see sows/activity-weather-conditions.md).

import (
	"context"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/weather"
)

// weatherCaptureTimeout bounds the detached capture. Generous relative to the
// provider's 8s HTTP client timeout so a retry-free slow call still lands,
// tight enough that a hung provider cannot accumulate one goroutine per
// upload.
const weatherCaptureTimeout = 30 * time.Second

// WeatherCapturer is the seam onto internal/weather's activity capture. The
// activity package owns WHEN a capture happens; the weather package owns the
// budget, the provider, and the store.
type WeatherCapturer interface {
	Capture(ctx context.Context, activityID string, lat, lon float64, at time.Time) error
	Get(ctx context.Context, activityID string) (*weather.ActivityWeather, error)
}

// SetWeatherCapturer wires the conditions capture in. Injected
// post-construction so NewHandler's signature — and every test that calls it —
// stays untouched. Safe to never call: capture is best-effort and nil-guarded,
// and the detail read simply omits the weather key.
func (h *Handler) SetWeatherCapturer(c WeatherCapturer) { h.weather = c }

// firstPosition returns the coordinate of the first positioned trackpoint —
// where the activity started. A 10 km loop sits inside a single weather grid
// cell, so the start point is not a lossy approximation of the run; it is the
// run's weather.
func firstPosition(tps []Trackpoint) (lat, lon float64, ok bool) {
	for _, tp := range tps {
		if tp.Latitude != nil && tp.Longitude != nil {
			return *tp.Latitude, *tp.Longitude, true
		}
	}
	return 0, 0, false
}

// captureWeather spawns the detached conditions capture. Three properties are
// load-bearing and each has a test:
//
//   - It runs on context.Background(), NOT r.Context(). The request context is
//     cancelled the instant the upload response is written, so a fetch
//     inheriting it would be cancelled essentially always — a feature that
//     silently never works, the exact shape of the WHOOP webhook outage.
//   - It is bounded, so a hanging provider cannot leak a goroutine per upload.
//   - It never blocks the response and never surfaces a failure to the
//     uploader. TCX import is already the slowest user-facing write in the
//     product; a third-party call on that path would make a slow OpenWeather
//     day look like a broken uploader.
//
// A failure leaves NO ROW, so the activity stays indistinguishable from one
// never attempted and cmd/weather-backfill is the single repair path.
func (h *Handler) captureWeather(a Activity) {
	if h.weather == nil || a.Environment != EnvironmentOutdoor {
		return
	}
	// Checked before the goroutine: an activity with no position has nothing
	// to ask about, and the terminal no_coordinates row is the backfill's to
	// write, not the import path's.
	lat, lon, ok := firstPosition(a.Trackpoints)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), weatherCaptureTimeout)
		defer cancel()
		_ = h.weather.Capture(ctx, a.ID, lat, lon, a.StartTime)
	}()
}
