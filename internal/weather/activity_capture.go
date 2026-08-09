package weather

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
)

// ErrCaptureDisabled is returned when capture is switched off — either the
// master weather flag, the capture knob, or an unconfigured provider. It is a
// distinct error rather than a silent no-op so the backfill can say why it did
// nothing instead of reporting a successful run that captured zero activities.
var ErrCaptureDisabled = errors.New("weather: activity weather capture is disabled")

// ActivityCapturer captures and stores the conditions at one activity's start.
// It is the only writer of activity_weather 'ok' rows on the import path, and
// the backfill drives the same method so there is one code path to trust.
type ActivityCapturer struct {
	cfg      config.WeatherConfig
	repo     ActivityWeatherRepository
	ledger   *BudgetLedger
	provider Provider
	log      *slog.Logger
	// now is injectable so tests can pin fetched_at without sleeping.
	now func() time.Time
}

func NewActivityCapturer(cfg config.WeatherConfig, repo ActivityWeatherRepository, ledger *BudgetLedger, provider Provider, log *slog.Logger) *ActivityCapturer {
	return &ActivityCapturer{cfg: cfg, repo: repo, ledger: ledger, provider: provider, log: log, now: time.Now}
}

// Enabled reports whether a capture would do anything. The master `enabled`
// switch gates the capture knob: `enabled = false` means the vendor
// integration is off entirely, so it cannot be overridden per-feature.
func (c *ActivityCapturer) Enabled() bool {
	return c.cfg.Enabled && c.cfg.CaptureActivityWeather && c.provider.Configured()
}

// Capture fetches the conditions at `at` for one coordinate and stores them as
// a terminal 'ok' row.
//
// Every failure path deliberately leaves NO ROW: the activity stays
// indistinguishable from one that was never attempted, which makes
// cmd/weather-backfill the single repair path for both a five-year history and
// yesterday's timeout. The one exception it does not own is the definitive
// negative — ErrNoHistoricalData is returned unwritten, because only the
// backfill turns it into a terminal 'unavailable' row.
func (c *ActivityCapturer) Capture(ctx context.Context, activityID string, lat, lon float64, at time.Time) error {
	// Checked before the reservation so a disabled deploy never touches the
	// ledger — an operator who switches capture off during a backfill expects
	// the day's budget to stay untouched, not to be silently drawn down.
	if !c.Enabled() {
		return ErrCaptureDisabled
	}

	// The SAME ledger and ceiling the tile uses. The budget is an account-level
	// constraint against OpenWeather, so a second allowance would model a limit
	// that does not exist. Reserved BEFORE the call, so a crash over-counts by
	// one rather than spending unrecorded.
	switch err := c.ledger.Reserve(ctx, 1, activeCeiling(c.cfg)); {
	case errors.Is(err, ErrBudgetExhausted):
		// Not counted as `failed`. A budget stop is the clean, expected end of
		// a backfill day, and it already has dedicated gauges; folding it into
		// the band the dashboard reads as "stop and look" would make every
		// normal backfill look like an incident.
		return err
	case err != nil:
		activityCapturesTotal.WithLabelValues("failed").Inc()
		return err
	}

	// Round to the nearest hour: historical data is published hourly, so a
	// 14:07 start and a 14:40 start describe the 14:00 and 15:00 observations
	// respectively. The provider's own observed_at is what gets stored, so a
	// reading is never attributed to a timestamp it does not describe.
	started := c.now()
	obs, err := c.provider.Historical(ctx, lat, lon, at.UTC().Round(time.Hour))
	observeCall(EndpointHistorical, started, err, c.now)
	switch {
	case errors.Is(err, ErrNoHistoricalData):
		return err
	case err != nil:
		activityCapturesTotal.WithLabelValues("failed").Inc()
		c.log.WarnContext(ctx, "weather: activity capture failed",
			"activity_id", activityID, "error", err)
		return err
	}

	if err := c.repo.Put(ctx, ActivityWeather{
		ActivityID:  activityID,
		Lat:         &lat,
		Lon:         &lon,
		Status:      ActivityStatusOK,
		Observation: &obs,
		FetchedAt:   c.now().UTC(),
	}); err != nil {
		// A call was spent and the answer was lost, which is the one failure
		// here that costs money without leaving a trace anywhere else.
		activityCapturesTotal.WithLabelValues("failed").Inc()
		c.log.ErrorContext(ctx, "weather: activity weather write failed",
			"activity_id", activityID, "error", err)
		return err
	}
	activityCapturesTotal.WithLabelValues("ok").Inc()
	return nil
}

// RecordNoCoordinates writes the terminal 'no_coordinates' row for an activity
// with no positioned trackpoint. Terminal because position cannot be
// re-derived, and free because there is no provider call — which is also why
// it carries no Enabled() gate and no reservation: it records what is already
// known rather than asking the vendor anything.
func (c *ActivityCapturer) RecordNoCoordinates(ctx context.Context, activityID string) error {
	return c.recordTerminal(ctx, ActivityWeather{
		ActivityID: activityID,
		Status:     ActivityStatusNoCoordinates,
		FetchedAt:  c.now().UTC(),
	})
}

// RecordUnavailable writes the terminal 'unavailable' row after the provider's
// definitive negative. lat/lon are stored even though there is no reading, so
// --retry-unavailable can tell a coordinate that was tried from one that was
// never resolvable.
func (c *ActivityCapturer) RecordUnavailable(ctx context.Context, activityID string, lat, lon float64) error {
	return c.recordTerminal(ctx, ActivityWeather{
		ActivityID: activityID,
		Lat:        &lat,
		Lon:        &lon,
		Status:     ActivityStatusUnavailable,
		FetchedAt:  c.now().UTC(),
	})
}

// recordTerminal writes one no-reading row and counts it under its own status,
// so the capture counter's `failed` band stays reserved for outcomes that
// wasted budget or need looking at.
func (c *ActivityCapturer) recordTerminal(ctx context.Context, row ActivityWeather) error {
	if err := c.repo.Put(ctx, row); err != nil {
		activityCapturesTotal.WithLabelValues("failed").Inc()
		c.log.ErrorContext(ctx, "weather: activity weather write failed",
			"activity_id", row.ActivityID, "status", string(row.Status), "error", err)
		return err
	}
	activityCapturesTotal.WithLabelValues(string(row.Status)).Inc()
	return nil
}

// Get passes the stored row straight through, so the activity handler needs
// exactly one seam onto this package rather than one for reads and one for
// writes.
func (c *ActivityCapturer) Get(ctx context.Context, activityID string) (*ActivityWeather, error) {
	return c.repo.Get(ctx, activityID)
}
