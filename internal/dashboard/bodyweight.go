package dashboard

import (
	"sort"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
)

// bwSparkMax is the maximum number of points in the bodyweight trend sparkline.
const bwSparkMax = 8

// buildBodyweight assembles the bodyweight tile from the user's measurements
// and goal. It is pure (no DB, no clock). entries are documented newest-first
// but we don't rely on it: current is the max-MeasuredAt entry and the spark
// is sorted oldest→newest. Returns nil when there are no entries.
func buildBodyweight(entries []bodyweight.Entry, goal bodyweight.Goal) *BodyweightSection {
	if len(entries) == 0 {
		return nil
	}

	// Sort a copy oldest→newest so we don't mutate the caller's slice.
	sorted := make([]bodyweight.Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].MeasuredAt.Before(sorted[j].MeasuredAt)
	})

	newest := sorted[len(sorted)-1]

	weights := make([]float64, len(sorted))
	for i := range sorted {
		weights[i] = sorted[i].Weight
	}

	return &BodyweightSection{
		Current:     newest.Weight,
		Unit:        string(newest.Unit),
		RatePerWeek: ratePerWeek(sorted),
		Goal:        bodyweightGoal(goal),
		TrendSpark:  downsampleFloats(weights, bwSparkMax),
	}
}

// ratePerWeek is the least-squares regression slope of weight vs. time (in
// days) scaled to per-week. It answers "how fast is the user trending" more
// robustly than first-vs-last because it uses every point. nil when there are
// fewer than 2 points or every measurement shares one instant (zero span,
// undefined slope). entries must be sorted oldest→newest.
func ratePerWeek(entries []bodyweight.Entry) *float64 {
	n := len(entries)
	if n < 2 {
		return nil
	}

	// x is days since the first measurement; using a relative origin keeps
	// the sums small and the slope unchanged.
	base := entries[0].MeasuredAt
	var sumX, sumY, sumXY, sumXX float64
	for _, e := range entries {
		x := e.MeasuredAt.Sub(base).Hours() / 24
		y := e.Weight
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}

	fn := float64(n)
	denom := fn*sumXX - sumX*sumX
	if denom == 0 {
		// All measurements at the same instant: slope undefined.
		return nil
	}
	slopePerDay := (fn*sumXY - sumX*sumY) / denom
	rate := slopePerDay * 7
	return &rate
}

// bodyweightGoal returns the goal struct, or nil when unset. The read path
// represents "never set" as the zero value with a nil CreatedAt.
func bodyweightGoal(goal bodyweight.Goal) *BodyweightGoal {
	if goal.CreatedAt == nil {
		return nil
	}
	return &BodyweightGoal{
		Weight: goal.Weight,
		Unit:   string(goal.Unit),
	}
}
