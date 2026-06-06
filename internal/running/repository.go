package running

import (
	"context"
	"time"
)

// Repository persists running sessions and their downsampled trackpoint
// streams. Implementations are in-memory (dev/test default) or SQLite
// (prod). All methods enforce ownership at the storage layer so handlers
// don't have to remember a user_id WHERE clause; passing a wrong user_id
// returns ErrNotFound rather than 200 on someone else's row.
//
// The original TCX file is archived to object storage (S3 in prod). The
// repository is constructed WITH an Archiver so the write ordering —
// insert the row, then archive, then commit — lives in one place.
type Repository interface {
	// Create generates s.ID, s.TCXS3Key ("runs/<user_id>/<id>.tcx") and
	// s.CreatedAt, then inserts the session plus its Trackpoints in one
	// transaction and archives the raw tcx bytes. On a duplicate
	// (user_id, garmin_activity_id) it returns ErrDuplicate; on an
	// archive failure it returns ErrStorage and persists nothing. On
	// success the generated fields are populated on the passed pointer.
	Create(ctx context.Context, s *Session, tcx []byte) error

	// GetByGarminActivityID returns the live session matching the dedup
	// key, for the handler's 409 lookup. Trackpoints are not loaded.
	// Returns ErrNotFound when no live row matches.
	GetByGarminActivityID(ctx context.Context, userID, garminActivityID string) (*Session, error)

	// List returns live sessions newest-first by start_time, capped at
	// limit. When before != nil, only sessions with start_time < before
	// are returned (keyset pagination). Trackpoints are not loaded.
	List(ctx context.Context, userID string, limit int, before *time.Time) ([]Session, error)

	// ListInRange returns every live session whose start_time falls in the
	// half-open interval [since, until), newest-first. Either bound may be
	// nil to mean "open-ended on that side." This is the calendar's
	// month-view path; no cursor is returned since the bounds already cap
	// the result. Trackpoints are not loaded.
	ListInRange(ctx context.Context, userID string, since, until *time.Time) ([]Session, error)

	// Get returns one live session WITH its Trackpoints ordered by
	// sequence. Returns ErrNotFound when the session is missing,
	// soft-deleted, or owned by another user.
	Get(ctx context.Context, userID, id string) (*Session, error)

	// Rename updates a live session's name and returns the updated
	// session (without trackpoints). Returns ErrNotFound when no live
	// row matches.
	Rename(ctx context.Context, userID, id, name string) (*Session, error)

	// SoftDelete stamps deleted_at on a live session. The S3 object and
	// trackpoints are left untouched. Returns ErrNotFound when no live
	// row matches.
	SoftDelete(ctx context.Context, userID, id string) error

	// Metrics aggregates the dashboard stat tiles over the user's live
	// sessions. Week/month boundaries are computed in loc (the user's
	// IANA timezone) since a calendar week/month is a local-time concept.
	Metrics(ctx context.Context, userID string, now time.Time, loc *time.Location) (Metrics, error)
}

// PeriodStat is a distance + run-count rollup over some window of
// sessions. Used for the current-week, current-month and all-time tiles.
type PeriodStat struct {
	DistanceMeters float64
	RunCount       int
}

// Metrics carries the four dashboard rollups. The handler maps these to
// the JSON shape:
//
//	{ current_week:{distance_meters,run_count,delta_pct_vs_prior_week},
//	  current_month:{distance_meters,run_count},
//	  recent_avg_pace_sec_per_km,
//	  all_time:{distance_meters,run_count} }
type Metrics struct {
	CurrentWeek PeriodStat
	// DeltaPctVsPriorWeek is the percent change in current-week distance
	// versus the prior local week. nil when the prior week's distance is
	// zero (no baseline to divide by).
	DeltaPctVsPriorWeek *float64

	CurrentMonth PeriodStat

	// RecentAvgPaceSecPerKm is the aggregate pace over the last 30 days:
	// sum(duration_seconds) / (sum(distance_meters) / 1000). nil when no
	// session in the window carries distance.
	RecentAvgPaceSecPerKm *float64

	AllTime PeriodStat
}
