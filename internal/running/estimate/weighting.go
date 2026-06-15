package estimate

import (
	"math"
	"time"
)

// This file holds the two per-point likelihood weights and the human-facing
// source classifier. Both weights are multiplied together to scale a point's
// precision contribution in riegel_bayes.go — a down-weighted point moves
// the posterior less. They are kept here, pure and tiny, so the weighting
// policy can be tuned and unit-tested in isolation from the regression math.

// recencyWeight decays a point's influence with age using a smooth
// exponential. tauDays is the e-folding constant (~half-life-ish): a point
// tauDays old carries weight 1/e ≈ 0.37 of a brand-new one. We clamp the age
// at zero so a future-dated effort (clock skew, bad import) is treated as
// "now" rather than amplified.
func recencyWeight(now, achievedAt time.Time) float64 {
	dtDays := math.Max(0, now.Sub(achievedAt).Hours()/24)
	return math.Exp(-dtDays / tauDays)
}

// qualityWeight down-weights efforts that were almost certainly sub-maximal
// because they are a small window carved out of a much longer run — e.g. the
// fastest 5k inside a 15k easy run is rarely a true 5k race effort. The
// signal is the ratio of effort distance to total activity distance: at or
// above qualityKnee the effort fills (most of) the run and earns full weight;
// as the ratio shrinks the weight falls linearly toward qualityFloor (never
// zero — a long-run window is weak evidence, not no evidence). When the
// activity distance is unknown (0) we cannot judge, so we assume full weight.
//
// v1 limitation (see README): this is a distance-ratio heuristic only. A
// pace-ratio refinement (was the window actually run hard?) is a fast follow.
func qualityWeight(effortMeters, activityMeters float64) float64 {
	if activityMeters <= 0 {
		return 1.0
	}
	ratio := clamp(effortMeters/activityMeters, 0, 1)
	return qualityFloor + (1-qualityFloor)*clamp(ratio/qualityKnee, 0, 1)
}

// ClassifySource is the label behind qualityWeight: an effort that fills the
// run (or whose activity distance is unknown) reads as "race_like"; a small
// window of a longer run reads as "long_run_window". Purely informational —
// the regression uses qualityWeight's continuous value, not this string.
func ClassifySource(effortMeters, activityMeters float64) string {
	if activityMeters <= 0 {
		return "race_like"
	}
	if effortMeters/activityMeters >= qualityKnee {
		return "race_like"
	}
	return "long_run_window"
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
