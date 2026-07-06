// Package estimate is the running max-effort estimation engine. It predicts
// the time an athlete would run for a standard distance if they raced it
// today — a forward-looking model output, deliberately distinct from the
// historical best efforts the activity layer extracts from past runs.
//
// The package is intentionally self-contained: it imports nothing from the
// HTTP, DB, or activity layers and operates purely on the plain input
// structs defined here. Every exported function is a pure function of its
// arguments (the only "now" it sees is injected via EstimateInput.Now), so
// the whole engine is trivially unit-testable and back-testable.
package estimate

import "time"

// EstimatorVersion is stamped onto every result so output is traceable and
// caches/labels can react to model changes. Bump on any behavioral change
// (see README "How to iterate").
const EstimatorVersion = "2.0.0"

// Estimator is the engine seam. v2 is riegelBayes; callers depend on this
// interface so a future model can be swapped in without touching call sites.
type Estimator interface {
	Estimate(in EstimateInput) EstimateResult
}

// Attempt is one usable max-effort data point: a (distance, time) pair the
// athlete actually produced, plus provenance the quality heuristics need.
type Attempt struct {
	DistanceKey             string
	DistanceMeters          float64
	DurationSeconds         float64
	AchievedAt              time.Time
	ActivityDistanceMeters  float64 // total distance of the source run (0 = unknown)
	ActivityAvgPaceSecPerKm *float64
	WindowStartElapsed      *float64 // seconds into the source activity; nil = unknown
	WindowEndElapsed        *float64
	HRZoneHighIntensityPct  *float64 // fraction of window time in zones 4–5; nil = unknown
	IsCurrentBestAtDistance bool     // true for the per-distance anchor row
}

// Demographics is the optional athlete context that anchors the prior level
// when there is little or no effort history. Every field is optional.
type Demographics struct {
	Age      *int
	Sex      *string // "male" | "female"
	WeightKg *float64
	HeightCm *float64
}

// EstimateInput is the full, self-contained request to the engine.
type EstimateInput struct {
	TargetDistanceKey    string
	TargetDistanceMeters float64
	Attempts             []Attempt
	Demographics         Demographics
	Now                  time.Time
	// LoggedBestSeconds is the user's current logged best at the target
	// distance, when one exists. Drives the logged-best floor post-process.
	LoggedBestSeconds *float64
}

// EstimateResult is the engine's prediction plus provenance for honest UI
// labelling.
type EstimateResult struct {
	Seconds             float64
	LowerSeconds        float64
	UpperSeconds        float64
	RawSeconds          float64 // pre-floor model output; 0 when insufficient_data
	FlooredAtLoggedBest bool
	Basis               string // insufficient_data | demographic_prior | single_effort | fitted_curve | logged_best_floor
	Confidence          string // low | medium | high
	NPoints             int
	NDistances          int
	Version             string
}

// NewEstimator returns the current production model (v2: riegelBayes).
func NewEstimator() Estimator {
	return riegelBayes{}
}
