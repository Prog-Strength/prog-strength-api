package strength

import (
	"math"
	"time"
)

// EpleyOneRM returns the Epley estimated 1RM for a set:
//
//	1RM = weight × (1 + reps/30)
//
// Epley was picked over Brzycki for two reasons: it's the most widely
// used in lifting apps and literature (familiarity), and it stays
// well-defined down to 1 rep where Brzycki has a discontinuity near
// reps=37. For reps=1 the formula collapses to the raw weight, which
// matches reality — a 1-rep set IS a 1RM. For very high reps (>10-12)
// the estimate is noisy across all formulas; we don't filter those
// out and accept some variance in the resulting trend.
func EpleyOneRM(weight float64, reps int) float64 {
	if reps <= 1 {
		return weight
	}
	return weight * (1.0 + float64(reps)/30.0)
}

// Trendline is two endpoints on a least-squares line, evaluated at
// the query's `since` and `until`. Returning ready-to-plot endpoints
// (rather than slope/intercept) means the frontend can render the
// line with two coordinates without re-deriving the regression math.
type Trendline struct {
	StartAt    time.Time `json:"start_at"`
	StartValue float64   `json:"start_value"`
	EndAt      time.Time `json:"end_at"`
	EndValue   float64   `json:"end_value"`
}

// round1 rounds to one decimal place. Keeps absolute 1RM numbers in
// the JSON output readable ("232.7") rather than carrying float
// precision the user would never notice ("232.66666666666669").
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
