package activity

import "time"

// Activity is the summary of a single recorded activity (a run, walk,
// bike, etc.), persisted to the activities table. It is derived from an
// ingested TCX file by the parser + summarizer; the heavy per-second
// trackpoint stream is stored separately and only loaded for the detail
// view.
//
// Activity is the sport-agnostic generalization of the prior running-only
// Session type. Running-specific summary fields (avg/best pace, HR, etc.)
// are kept here as nullable fields rather than split into a per-sport
// detail table; they're populated only when ActivityType is Running.
// Walks, rides, and other sports leave them nil.
//
// Pointer fields are nil when either the source did not carry that signal
// (e.g. a watch without a chest strap omits heart rate) or the metric
// doesn't apply to the sport. A nil pointer means "unknown / not
// applicable", deliberately distinct from a zero value.
type Activity struct {
	ID     string
	UserID string
	// ActivityType is the normalized sport. See activity_type.go.
	ActivityType ActivityType
	// IngestSource tags how this activity entered the system (manual TCX
	// upload today; Garmin Connect API in the future). See ingest_source.go.
	IngestSource IngestSource
	// SourceActivityID is the activity identifier the source assigned to
	// this row (the <Id> element for TCX, the Garmin Connect activity ID
	// for the API). Combined with (user_id, ingest_source) it's the dedup
	// key for re-ingests.
	SourceActivityID string
	StartTime        time.Time
	Name             *string
	DistanceMeters   float64
	// RawDistanceMeters is the distance as originally ingested. Set equal to
	// DistanceMeters at ingest and never touched by calibration, so
	// "calibrated" is derivable as RawDistanceMeters != DistanceMeters and a
	// reset is a calibrate back to this value.
	RawDistanceMeters float64
	// Environment is 'outdoor' (GPS) or 'indoor' (treadmill/no-position).
	// Defaulted at ingest for running activities from Position presence;
	// user-overridable via PATCH.
	Environment         Environment
	DurationSeconds     int
	AvgPaceSecPerKm     *float64
	BestPaceSecPerKm    *float64
	AvgHeartRateBpm     *int
	MaxHeartRateBpm     *int
	TotalCalories       *int
	ElevationGainMeters *float64
	// RouteGeoJSON is the serialized GeoJSON Feature (MultiLineString +
	// bounds) for the simplified GPS route, computed at ingest from the raw
	// positioned series. nil when the activity had fewer than two positioned
	// points (indoor / no-GPS). Loaded only on the detail path, never on
	// list/summary reads.
	RouteGeoJSON *string
	TCXS3Key     string
	CreatedAt    time.Time
	// Soft delete, consistent with the rest of the repo. json:"-" lives
	// on the API DTO, not here; this is the in-memory domain type.
	DeletedAt *time.Time
	// Trackpoints is populated only on the detail load path. On list
	// responses it stays nil to avoid shipping thousands of points.
	Trackpoints []Trackpoint
	// BestEfforts holds the fastest window of each standard distance found
	// inside this activity (1mi/2mi/5k/…). Populated only for running
	// activities by the summarizer; empty for walks/rides and for runs too
	// short to cover even the 1-mile target. See best_efforts.go.
	BestEfforts []ActivityBestEffort
}

// Trackpoint is one downsampled sample along an activity's track. The raw
// TCX stream is ~1 Hz (thousands of points); the summarizer reduces it
// to ~300 points for charting. Sequence is the kept-point index, not the
// original sample index.
type Trackpoint struct {
	Sequence        int
	ElapsedSeconds  int
	DistanceMeters  float64
	HeartRateBpm    *int
	PaceSecPerKm    *float64
	ElevationMeters *float64
	// Latitude/Longitude are the WGS84 coordinates of this kept sample,
	// truncated to 6 decimals, when the source trackpoint had a <Position>;
	// nil otherwise. The map renders from Activity.RouteGeoJSON, not these —
	// they are stored for future pace↔map correlation / FIT parity.
	Latitude  *float64
	Longitude *float64
}
