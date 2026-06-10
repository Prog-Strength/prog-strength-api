package activity

import (
	"math"
	"time"
)

// StandardDistance is one of the fixed-set targets the best-effort sweep
// produces a row for on every running activity. The Key is the value
// stored in activity_best_efforts.distance_key — keep it stable across
// releases since the DB CHECK constraint references it by exact string
// (TestStandardDistances_MatchMigrationCheck guards the two against drift).
type StandardDistance struct {
	Key         string
	Meters      float64
	DisplayName string
}

// StandardDistances is the v1 set. Order is display order (shortest
// first); downstream code that iterates (the bests DTO, the sweep result)
// assumes this order, but no algorithm depends on it.
var StandardDistances = []StandardDistance{
	{Key: "1mi", Meters: 1609.344, DisplayName: "1 Mile"},
	{Key: "2mi", Meters: 3218.688, DisplayName: "2 Mile"},
	{Key: "5k", Meters: 5000, DisplayName: "5K"},
	{Key: "10k", Meters: 10000, DisplayName: "10K"},
	{Key: "half_marathon", Meters: 21097.5, DisplayName: "Half Marathon"},
	{Key: "marathon", Meters: 42195, DisplayName: "Marathon"},
}

// bestEffortsVersion is bumped when the sweep algorithm changes in a way
// that requires existing rows to be recomputed. It's not stored per-row
// today (see SOW Open Question #1) — it's the constant a future
// "force recompute" backfill branch tests against.
const bestEffortsVersion = 1

// ActivityBestEffort is one (distance, fastest-window-time) result of the
// sweep over a single activity. Hung off Activity.BestEfforts by the
// summarizer and written to activity_best_efforts by the repository.
type ActivityBestEffort struct {
	DistanceKey     string
	DurationSeconds float64
}

// bestEfforts runs a distance-anchored two-pointer sweep over the raw
// trackpoint stream for each target distance, returning the minimum-time
// window that covers each distance. Results come back in targets order
// (shortest first); a target whose distance exceeds the activity's total
// cumulative distance is omitted entirely.
//
// The sweep operates on the raw (un-downsampled) trackpoints — the ~300
// point chart downsample strides 50–150 m apart on a typical run, far too
// coarse for honest 1-mile-window math. summarize hands us the raw slice
// it already has in memory.
//
// For each window covering >= T meters the right edge is linearly
// interpolated to the exact crossing of T, removing the systematic bias
// that "first window to meet-or-exceed T" introduces (samples are 5–15 m
// apart, so a target of exactly 5000 m almost never lands on a boundary).
//
// Accepted limitations, documented per SOW non-goals:
//   - Paused/stopped time is treated as elapsed: a 30 s traffic-light stop
//     inside a window is baked into that window's duration. Garmin's TCX
//     stream carries no consistent "paused" signal, so moving-time
//     separation is out of scope for v1.
//   - Only the right edge is interpolated. The left edge is anchored on
//     sample boundaries, leaving a fractional-sample-period asymmetric
//     bias (well under a second at typical ~1 Hz rates). Symmetrizing is a
//     clean follow-up; the residual is imperceptible for v1.
func bestEfforts(tps []parsedTrackpoint, targets []StandardDistance) []ActivityBestEffort {
	if len(tps) < 2 {
		return nil
	}
	total := tps[len(tps)-1].DistanceMeters - tps[0].DistanceMeters

	out := make([]ActivityBestEffort, 0, len(targets))
	for _, target := range targets {
		T := target.Meters
		// Activities shorter than the target produce no row for it.
		if total < T {
			continue
		}

		best := math.Inf(1)
		left := 0
		for right := 1; right < len(tps); right++ {
			// Tighten the left edge while [left+1, right] still covers T.
			for left+1 < right && tps[right].DistanceMeters-tps[left+1].DistanceMeters >= T {
				left++
			}
			span := tps[right].DistanceMeters - tps[left].DistanceMeters
			if span < T {
				continue // right hasn't extended far enough yet.
			}

			// The crossing of tps[left].dist + T falls inside the right-most
			// segment [prev, right]: the loop guarantees [left, right-1]
			// spans < T, so the target distance is reached only between
			// prev = max(left, right-1) and right.
			prev := right - 1
			if left > prev {
				prev = left
			}
			segD := tps[right].DistanceMeters - tps[prev].DistanceMeters
			if segD <= 0 {
				continue // degenerate (non-strict monotonic) zero-distance segment.
			}
			targetEnd := tps[left].DistanceMeters + T
			ratio := (targetEnd - tps[prev].DistanceMeters) / segD
			endT := tps[prev].Time.Add(time.Duration(ratio * float64(tps[right].Time.Sub(tps[prev].Time))))
			windowS := endT.Sub(tps[left].Time).Seconds()
			if windowS < best {
				best = windowS
			}
		}

		if math.IsInf(best, 1) {
			// Defensive: total >= T should always yield at least one window,
			// but zero-distance segments at the boundary could theoretically
			// leave best unset. Omit rather than emit +Inf.
			continue
		}
		out = append(out, ActivityBestEffort{DistanceKey: target.Key, DurationSeconds: best})
	}
	return out
}
