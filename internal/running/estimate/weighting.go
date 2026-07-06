package estimate

import (
	"math"
	"time"
)

// Combined per-point weight for the v2 model. Each factor lives in its own
// function so the policy can be tuned and tested in isolation.
func attemptWeight(now time.Time, a Attempt) float64 {
	w := recencyWeight(now, a.AchievedAt)
	w *= qualityWeight(a.DistanceMeters, a.ActivityDistanceMeters)
	w *= paceRatioWeight(a.DistanceMeters, a.DurationSeconds, a.ActivityAvgPaceSecPerKm)
	w *= hrIntensityWeight(a.HRZoneHighIntensityPct)
	if a.IsCurrentBestAtDistance {
		w *= anchorBoost
	}
	return w
}

// recencyWeight decays a point's influence with age using a smooth
// exponential. tauDays is the e-folding constant.
func recencyWeight(now, achievedAt time.Time) float64 {
	dtDays := math.Max(0, now.Sub(achievedAt).Hours()/24)
	return math.Exp(-dtDays / tauDays)
}

// qualityWeight down-weights efforts that are small windows of longer runs.
func qualityWeight(effortMeters, activityMeters float64) float64 {
	if activityMeters <= 0 {
		return 1.0
	}
	ratio := clamp(effortMeters/activityMeters, 0, 1)
	return qualityFloor + (1-qualityFloor)*clamp(ratio/qualityKnee, 0, 1)
}

// paceRatioWeight down-weights windows slower than the source activity's
// average pace (likely not a max effort).
func paceRatioWeight(effortMeters, durationSeconds float64, activityAvgPaceSecPerKm *float64) float64 {
	if activityAvgPaceSecPerKm == nil || *activityAvgPaceSecPerKm <= 0 || effortMeters <= 0 || durationSeconds <= 0 {
		return 1.0
	}
	windowPace := durationSeconds / (effortMeters / 1000)
	ratio := windowPace / *activityAvgPaceSecPerKm
	if ratio <= 1.0 {
		return 1.0
	}
	// ratio 1.0 -> 1.0; ratio >= paceRatioKnee -> paceRatioFloor
	return paceRatioFloor + (1-paceRatioFloor)*clamp((paceRatioKnee-ratio)/(paceRatioKnee-1.0), 0, 1)
}

// hrIntensityWeight scales precision by the fraction of window time spent in
// zones 4–5 (when known). Unknown HR is neutral (1.0).
func hrIntensityWeight(zoneHighPct *float64) float64 {
	if zoneHighPct == nil {
		return 1.0
	}
	pct := clamp(*zoneHighPct, 0, 1)
	if pct >= hrIntensityFullPct {
		return 1.0
	}
	return hrIntensityFloor + (1-hrIntensityFloor)*(pct/hrIntensityFullPct)
}

// ClassifySource is the human-facing label for an effort's quality.
func ClassifySource(effortMeters, activityMeters float64) string {
	if activityMeters <= 0 {
		return "race_like"
	}
	if effortMeters/activityMeters >= qualityKnee {
		return "race_like"
	}
	return "long_run_window"
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
