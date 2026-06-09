package activity

import "strings"

// ActivityType is the closed, sport-agnostic taxonomy stored on every
// Activity. New values require a code change and a CHECK-constraint
// migration on the activities table — adding a value silently here would
// let inserts pass Go validation and fail at the DB.
type ActivityType string

const (
	ActivityRunning ActivityType = "running"
	ActivityWalking ActivityType = "walking"
	ActivityCycling ActivityType = "cycling"
	ActivityOther   ActivityType = "other"
)

// Valid reports whether t is one of the known members. Used by the S3
// key builder and any future validator that takes an ActivityType from
// untrusted input.
func (t ActivityType) Valid() bool {
	switch t {
	case ActivityRunning, ActivityWalking, ActivityCycling, ActivityOther:
		return true
	}
	return false
}

// normalizeActivityType maps a raw sport string from the ingest source
// onto the closed ActivityType enum. The mapping is source-specific
// because each integration has its own taxonomy: Garmin's TCX sport
// attribute uses "Running"/"Biking"/"Other", while Garmin Connect's API
// (when wired up) uses an entirely different vocabulary.
//
// Unknown values fall through to ActivityOther rather than failing the
// ingest — the activity still gets recorded; the user can rename or
// reclassify if it matters. This keeps the import path tolerant; the
// catalog of known sports can grow without backfilling old uploads.
func normalizeActivityType(sport string, source IngestSource) ActivityType {
	switch source {
	case IngestManualTCX:
		// TCX <Activity Sport="..."> values are PascalCase per the
		// Garmin schema. Lowercase-compare so a tweaked exporter
		// emitting "running" still maps cleanly.
		switch strings.ToLower(strings.TrimSpace(sport)) {
		case "running":
			return ActivityRunning
		case "biking":
			return ActivityCycling
		case "other", "":
			return ActivityOther
		default:
			return ActivityOther
		}
	case IngestGarminAPI:
		// Garmin Connect uses a different taxonomy ("running", "treadmill_running",
		// "cycling", "road_biking", "indoor_cycling", "walking", ...). Wiring
		// this up means deciding which Connect activityTypeKey values fold
		// into ActivityRunning vs ActivityWalking vs ActivityCycling, which
		// is a real product decision we haven't made. Until then, treat
		// the source as not-yet-implemented so a forgotten wire-up surfaces
		// loudly rather than silently classifying every Connect activity
		// as "other".
		panic("activity: normalizeActivityType: IngestGarminAPI not implemented")
	}
	return ActivityOther
}
