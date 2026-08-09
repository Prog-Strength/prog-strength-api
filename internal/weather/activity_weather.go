package weather

import (
	"context"
	"time"
)

// ActivityStatus is the disposition of one capture attempt. The set is closed
// and mirrors the migration's CHECK.
type ActivityStatus string

const (
	ActivityStatusOK            ActivityStatus = "ok"
	ActivityStatusNoCoordinates ActivityStatus = "no_coordinates" // terminal: position cannot be re-derived
	ActivityStatusUnavailable   ActivityStatus = "unavailable"    // terminal: provider has no answer
)

// ActivityWeather is one stored reading.
//
// Every reading column is NULL unless Status is ActivityStatusOK, so the whole
// reading hangs off one pointer rather than fifteen independently-nullable
// fields that callers would have to check in agreement. Observation also owns
// ObservedAt: the provider's observation hour is part of the reading, and a
// second copy beside it would be a second thing to keep in sync.
type ActivityWeather struct {
	ActivityID string
	// Lat/Lon record which coordinate was used, so changing the rule from
	// "start point" to something else later is a re-backfill rather than a
	// guess about what was measured. Nil for no_coordinates.
	Lat, Lon *float64
	Status   ActivityStatus
	// Observation is nil unless Status is ActivityStatusOK.
	Observation *Observation
	// FetchedAt is when we asked, as distinct from the observation hour.
	FetchedAt time.Time
}

// PendingActivity is one outdoor activity with no activity_weather row: the
// anti-join's answer, and the unit of work cmd/weather-backfill iterates.
type PendingActivity struct {
	ActivityID string
	StartTime  time.Time
	// Lat/Lon are the first positioned trackpoint, nil when the activity
	// recorded no position at all. Nil is not an error — it is what the
	// backfill turns into a terminal no_coordinates row without spending a
	// provider call.
	Lat, Lon *float64
}

// PendingCounts is the split cmd/weather-backfill --dry-run prints, and the
// number an operator approves before any money is spent:
//
//	Eligible      — pending activities WITH a start coordinate. One provider
//	                call each; this is the only figure that costs anything.
//	NoCoordinates — pending activities with no positioned trackpoint. Free:
//	                resolved to a terminal row with no call.
//
// The third figure in that line, "already captured", is CountOK — it counts
// rows that exist rather than activities that do not, so it does not belong to
// a type describing what is still pending.
type PendingCounts struct {
	Eligible      int
	NoCoordinates int
}

type ActivityWeatherRepository interface {
	Get(ctx context.Context, activityID string) (*ActivityWeather, error)
	Put(ctx context.Context, row ActivityWeather) error
	// CountOK / CountPending feed the two new gauges and --dry-run.
	CountOK(ctx context.Context) (int, error)
	CountPending(ctx context.Context) (PendingCounts, error)
	// ListPending returns eligible outdoor activities with no row, newest
	// first — recent runs are the ones the user is most likely to open. A
	// limit of zero or less means no limit, matching --limit 0.
	ListPending(ctx context.Context, limit int) ([]PendingActivity, error)
	// ClearUnavailable drops terminal 'unavailable' rows so they become
	// eligible again. no_coordinates rows are deliberately NOT cleared.
	ClearUnavailable(ctx context.Context) (int, error)
}
