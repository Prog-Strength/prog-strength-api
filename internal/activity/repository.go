package activity

import (
	"context"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/hrzones"
)

// Repository persists activities and their downsampled trackpoint
// streams. Implementations are in-memory (dev/test default) or SQLite
// (prod). All methods enforce ownership at the storage layer so handlers
// don't have to remember a user_id WHERE clause; passing a wrong user_id
// returns ErrNotFound rather than 200 on someone else's row.
//
// The original TCX file is archived to object storage (S3 in prod). The
// repository is constructed WITH an Archiver so the write ordering —
// insert the row, then archive, then commit — lives in one place.
type Repository interface {
	// Create generates a.ID, builds a.TCXS3Key via buildTCXKey, stamps
	// a.CreatedAt, then inserts the activity plus its Trackpoints in one
	// transaction and archives the raw tcx bytes. On a duplicate
	// (user_id, ingest_source, source_activity_id) it returns ErrDuplicate;
	// on an archive failure it returns ErrStorage and persists nothing.
	// On success the generated fields are populated on the passed pointer.
	Create(ctx context.Context, a *Activity, tcx []byte) error

	// GetBySourceActivityID returns the live activity matching the dedup
	// key, for the handler's 409 lookup. Trackpoints are not loaded.
	// Returns ErrNotFound when no live row matches.
	GetBySourceActivityID(ctx context.Context, userID string, source IngestSource, sourceActivityID string) (*Activity, error)

	// List returns live activities newest-first by start_time, capped at
	// limit. When before != nil, only activities with start_time < before
	// are returned (keyset pagination). Trackpoints are not loaded.
	List(ctx context.Context, userID string, limit int, before *time.Time) ([]Activity, error)

	// ListInRange returns every live activity whose start_time falls in
	// the half-open interval [since, until), newest-first. Either bound
	// may be nil to mean "open-ended on that side." This is the calendar's
	// month-view path; no cursor is returned since the bounds already cap
	// the result. Trackpoints are not loaded.
	ListInRange(ctx context.Context, userID string, since, until *time.Time) ([]Activity, error)

	// Get returns one live activity WITH its Trackpoints ordered by
	// sequence. Returns ErrNotFound when the activity is missing,
	// soft-deleted, or owned by another user.
	Get(ctx context.Context, userID, id string) (*Activity, error)

	// SummariesByIDs returns the user's live activities whose id is in ids,
	// keyed by id and WITHOUT their trackpoint streams. Unlike List it does
	// not exclude strength_training rows — it's the batch read the workout
	// list endpoint uses to embed each workout's lightweight HR/calorie
	// enrichment. IDs that don't resolve to a live row are simply absent.
	SummariesByIDs(ctx context.Context, userID string, ids []string) (map[string]Activity, error)

	// Rename updates a live activity's name and returns the updated
	// activity (without trackpoints). Returns ErrNotFound when no live
	// row matches.
	Rename(ctx context.Context, userID, id, name string) (*Activity, error)

	// SoftDelete stamps deleted_at on a live activity. The S3 object and
	// trackpoints are left untouched. Returns ErrNotFound when no live
	// row matches.
	SoftDelete(ctx context.Context, userID, id string) error

	// RunningMetrics aggregates the dashboard stat tiles over the user's
	// live ActivityRunning rows only. Walks/rides don't contribute — the
	// running dashboard is sport-specific. Week/month boundaries are
	// computed in loc (the user's IANA timezone) since a calendar
	// week/month is a local-time concept.
	RunningMetrics(ctx context.Context, userID string, now time.Time, loc *time.Location) (Metrics, error)

	// GetUserRunningBestEfforts returns the user's current best across each
	// standard distance: one row per distance_key the user has ever
	// achieved, carrying the MIN(duration_seconds) and the activity that
	// produced it. Only live (deleted_at IS NULL) ActivityRunning rows
	// contribute. Distances the user has never covered are absent. On a
	// duration tie the earliest activity wins (preserves the original
	// date's claim, mirroring the lift-PR tie semantics).
	GetUserRunningBestEfforts(ctx context.Context, userID string) ([]RunningBestEffort, error)

	// GetRunningBestEffortHistory returns every best-effort row at the given
	// distance_key across the user's live running activities, ordered by
	// activity start_time ascending. The full series (not just record
	// breakers) so the progression chart shows real density.
	GetRunningBestEffortHistory(ctx context.Context, userID, distanceKey string) ([]BestEffortPoint, error)

	// ListRunningSamplesSince returns the (StartTime, DistanceMeters)
	// projection for the user's live ActivityRunning rows starting at/after
	// `since`. Walks/rides are excluded — the profile-stats distance series is
	// running-specific, mirroring RunningMetrics' filter. Bucketing into local
	// weeks happens in the handler.
	ListRunningSamplesSince(ctx context.Context, userID string, since time.Time) ([]RunSample, error)

	// RecentHRStats summarizes the user's recent running HR history for the
	// zone engine: over their non-deleted ActivityRunning rows with
	// start_time >= now-window and id != excludeActivityID, HistoryRunCount
	// is the number of those runs carrying at least one HR-bearing
	// trackpoint and RecentHRSamplesP99 is hrzones.P99 over all HR samples
	// across them (nil when none). CurrentRunP99 is left nil — the handler
	// fills it from the viewed run. excludeActivityID is typically the run
	// being viewed so its own samples don't bias its reference.
	RecentHRStats(ctx context.Context, userID string, window time.Duration, excludeActivityID string) (hrzones.Stats, error)
}

// RunSample is the minimal (start, distance) projection for one running
// activity, used to compute the weekly running-distance series in the
// profile-stats handler.
type RunSample struct {
	StartTime      time.Time
	DistanceMeters float64
}

// RunningBestEffort is one row of the per-user current-bests query: the
// user's fastest time at a distance plus the activity that set it.
type RunningBestEffort struct {
	DistanceKey       string
	DurationSeconds   float64
	ActivityID        string
	ActivityStartTime time.Time
}

// BestEffortPoint is one point in a single distance's progression series:
// the time achieved by one activity and that activity's start time.
type BestEffortPoint struct {
	ActivityID        string
	ActivityStartTime time.Time
	DurationSeconds   float64
	// ActivityDistanceMeters is the total distance of the source activity,
	// used by the max-effort estimator's quality weight.
	ActivityDistanceMeters float64
}

// PeriodStat is a distance + activity-count rollup over some window of
// activities. Used for the current-week, current-month and all-time tiles.
type PeriodStat struct {
	DistanceMeters float64
	RunCount       int
}

// Metrics carries the four dashboard rollups for the running domain. The
// handler maps these to the JSON shape:
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
	// activity in the window carries distance.
	RecentAvgPaceSecPerKm *float64

	AllTime PeriodStat
}
