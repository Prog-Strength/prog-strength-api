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
const EstimatorVersion = "1.0.0"

// Estimator is the engine seam. v1 is riegelBayes; callers depend on this
// interface so a future model can be swapped in without touching call sites.
type Estimator interface {
	Estimate(in EstimateInput) EstimateResult
}

// Attempt is one usable max-effort data point: a (distance, time) pair the
// athlete actually produced, plus the provenance the quality heuristic needs.
// These come from the activity layer's best-effort sweep, but this package
// neither knows nor cares about that — it sees only the plain numbers.
type Attempt struct {
	DistanceKey            string    // source distance of the effort ("5k", ...)
	DistanceMeters         float64   // distance the effort covered
	DurationSeconds        float64   // time taken over that distance
	AchievedAt             time.Time // when the effort happened (drives recency weight)
	ActivityDistanceMeters float64   // total distance of the run the effort came from (0 = unknown)
}

// Demographics is the optional athlete context that anchors the prior level
// when there is little or no effort history. Every field is optional; a
// missing field simply widens the prior (the model leans more on data).
type Demographics struct { // every field optional
	Age      *int     // years
	Sex      *string  // "male" | "female"
	WeightKg *float64 // body mass
	HeightCm *float64 // stature
}

// EstimateInput is the full, self-contained request to the engine. Attempts
// carries efforts at ALL distances (not just the target) because the curve
// fit gets its power from multi-distance evidence. Now is injected rather
// than read from the clock so recency weighting is deterministic and the
// engine can be replayed against historical snapshots for back-testing.
type EstimateInput struct {
	TargetDistanceKey    string       // which standard distance to predict ("marathon", ...)
	TargetDistanceMeters float64      // the target distance in meters
	Attempts             []Attempt    // ALL distances, not just the target
	Demographics         Demographics // optional; missing fields widen the prior
	Now                  time.Time    // injected for deterministic recency + back-testing
}

// EstimateResult is the engine's prediction plus enough provenance for a UI
// to label it honestly. The band is asymmetric because the model lives in
// log-time: a symmetric ±σ in log-space maps to an asymmetric interval in
// seconds (the slow tail is longer), which is the correct shape for race
// times. Basis and Confidence let the caller communicate how much to trust
// the number without re-deriving the math.
type EstimateResult struct {
	Seconds      float64 // point prediction for the target distance
	LowerSeconds float64 // confidence band (asymmetric — lognormal)
	UpperSeconds float64 // confidence band upper bound
	Basis        string  // "insufficient_data" | "demographic_prior" | "single_effort" | "fitted_curve"
	Confidence   string  // "low" | "medium" | "high" (derived from band width)
	NPoints      int     // count of usable efforts that fed the fit
	NDistances   int     // distinct distances among those efforts
	Version      string  // = EstimatorVersion
}

// NewEstimator returns the current production model (v1: riegelBayes).
func NewEstimator() Estimator {
	return riegelBayes{}
}
