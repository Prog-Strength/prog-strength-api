package running

import "time"

// Session is the summary of a single running activity, persisted to the
// running_sessions table. It is derived from an imported Garmin TCX file
// by the summarizer; the heavy per-second trackpoint stream is stored
// separately and only loaded for the detail view.
//
// Pointer fields are nil when the source TCX did not carry that signal
// (e.g. a watch without a chest strap omits heart rate). A nil pointer
// means "unknown", which is deliberately distinct from a zero value.
type Session struct {
	ID                  string
	UserID              string
	GarminActivityID    string
	StartTime           time.Time
	Name                *string
	DistanceMeters      float64
	DurationSeconds     int
	AvgPaceSecPerKm     float64
	BestPaceSecPerKm    *float64
	AvgHeartRateBpm     *int
	MaxHeartRateBpm     *int
	TotalCalories       *int
	ElevationGainMeters *float64
	TCXS3Key            string
	CreatedAt           time.Time
	// Soft delete, consistent with the rest of the repo. json:"-" lives
	// on the API DTO, not here; this is the in-memory domain type.
	DeletedAt *time.Time
	// Trackpoints is populated only on the detail load path. On list
	// responses it stays nil to avoid shipping thousands of points.
	Trackpoints []Trackpoint
}

// Trackpoint is one downsampled sample along a session's track. The raw
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
}
