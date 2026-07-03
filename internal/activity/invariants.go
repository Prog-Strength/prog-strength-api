package activity

import (
	"fmt"
	"math"
)

// Invariant tolerances. Everything the detail response renders is derived
// from one trackpoint stream, so disagreements beyond float/rounding noise
// mean a computation regressed (or a write path drifted, e.g. a calibrate
// that rescaled the summary but not the stream). Paces compare in
// sec-per-display-unit; distances in meters; times in seconds.
const (
	invariantDistTolMeters   = 0.5 // stored summary vs stream endpoint
	invariantSplitDistTol    = 0.01
	invariantTimeTolSeconds  = 1
	invariantPaceIdentityTol = 0.5
	invariantPaceMeanTol     = 1.0
	// I6 compares three window definitions (sample <= rolling unit <= aligned
	// full bucket). The bucket comparison is approximate at the edges (a
	// "full" split may span slightly less than one unit), so it gets slack.
	// bestRollingPace (like the pre-existing ingest-time bestPace it
	// generalizes) is a greedy distance-anchored sliding window: at each
	// right edge it always shrinks to the *tightest* window >= one bucket,
	// which is not guaranteed to be the true minimum-average window of that
	// length (trimming a disproportionately fast leading sample off a
	// slightly-overshooting window can raise its average). Random-track
	// fuzzing (TestInvariants_RandomTracksAlwaysAlign) observed this
	// approximation lag as high as ~9.5 sec/unit under synthetic GPS-jitter
	// bursts; the tolerance below covers that with headroom.
	invariantOrderingTol = 15.0
	invariantPctTol      = 0.01
)

// checkDetailInvariants verifies the assembled running-detail response
// reconciles with itself (SOW running-detail-metric-alignment, "Algorithms").
// It returns human-readable violation strings tagged I1..I7; empty means
// aligned. Callers log violations at ERROR and STILL serve the response — a
// read must never 500 over an accounting mismatch; CI fixtures assert
// emptiness so violations are caught as regressions.
func checkDetailInvariants(a Activity, d Derivation, unit DistanceUnit, zones *heartRateZonesDTO) []string {
	var v []string
	bucket := unit.BucketMeters()

	if len(a.Trackpoints) >= 2 {
		first, last := a.Trackpoints[0], a.Trackpoints[len(a.Trackpoints)-1]

		// I1: stored distance == stream endpoint, and split distances
		// telescope to the stream span.
		if math.Abs(last.DistanceMeters-a.DistanceMeters) > invariantDistTolMeters {
			v = append(v, fmt.Sprintf("I1: stored distance %.2f != last trackpoint %.2f", a.DistanceMeters, last.DistanceMeters))
		}
		var sumDist float64
		var sumTime int
		for _, s := range d.Splits {
			sumDist += s.DistanceMeters
			sumTime += s.DurationSeconds
		}
		if span := last.DistanceMeters - first.DistanceMeters; math.Abs(sumDist-span) > invariantSplitDistTol {
			v = append(v, fmt.Sprintf("I1: split distances sum %.2f != stream span %.2f", sumDist, span))
		}

		// I2: split times sum to the duration tile.
		if int(math.Abs(float64(sumTime-a.DurationSeconds))) > invariantTimeTolSeconds {
			v = append(v, fmt.Sprintf("I2: split times sum %d != duration %d", sumTime, a.DurationSeconds))
		}
	}

	// I3: every split pace equals time/dist (identity by construction; asserted so a
	// future "optimization" can't quietly reintroduce filtered pace math).
	for _, s := range d.Splits {
		if s.PaceSecPerUnit == nil || s.DistanceMeters <= 0 {
			continue
		}
		want := float64(s.DurationSeconds) / s.DistanceMeters * bucket
		if math.Abs(*s.PaceSecPerUnit-want) > invariantPaceIdentityTol {
			v = append(v, fmt.Sprintf("I3: split %d pace %.2f != time/dist %.2f", s.Index, *s.PaceSecPerUnit, want))
		}
	}

	// I4 + I5: the avg-pace tile is reproducible both from the splits
	// (distance-weighted mean == total time / total distance) and from the
	// stored duration/distance pair.
	if a.AvgPaceSecPerKm != nil && a.DistanceMeters > 0 {
		avgPerUnit := *a.AvgPaceSecPerKm * bucket / 1000
		var sumDist float64
		var sumTime int
		for _, s := range d.Splits {
			sumDist += s.DistanceMeters
			sumTime += s.DurationSeconds
		}
		if sumDist > 0 {
			weighted := float64(sumTime) / sumDist * bucket
			if math.Abs(weighted-avgPerUnit) > invariantPaceMeanTol {
				v = append(v, fmt.Sprintf("I4: weighted split pace %.2f != avg pace %.2f", weighted, avgPerUnit))
			}
		}
		recomputed := float64(a.DurationSeconds) / (a.DistanceMeters / 1000)
		if math.Abs(recomputed-*a.AvgPaceSecPerKm) > invariantPaceIdentityTol {
			v = append(v, fmt.Sprintf("I5: stored avg pace %.2f != duration/distance %.2f", *a.AvgPaceSecPerKm, recomputed))
		}
	}

	// I6: window-size ordering — fastest sample <= fastest rolling unit
	// window <= fastest full split (each contains the next as a candidate).
	// A "full" (non-partial) split only qualifies as a candidate when its
	// actual distance reaches the window size: the partial threshold allows
	// a trailing split down to 95% of a bucket to stay unflagged, but
	// bestRollingPace only ever considers windows >= one full bucket, so a
	// 95-99.9%-of-bucket trailing split is not a comparable window and must
	// be skipped here rather than treated as a violation.
	if d.BestPaceSecPerUnit != nil {
		if d.StripSummary.FastestSecPerUnit != nil &&
			*d.StripSummary.FastestSecPerUnit > *d.BestPaceSecPerUnit+invariantOrderingTol {
			v = append(v, fmt.Sprintf("I6: fastest sample %.2f slower than best window %.2f", *d.StripSummary.FastestSecPerUnit, *d.BestPaceSecPerUnit))
		}
		for _, s := range d.Splits {
			if s.Partial || s.PaceSecPerUnit == nil || s.DistanceMeters < bucket {
				continue
			}
			if *d.BestPaceSecPerUnit > *s.PaceSecPerUnit+invariantOrderingTol {
				v = append(v, fmt.Sprintf("I6: best window %.2f slower than full split %d pace %.2f", *d.BestPaceSecPerUnit, s.Index, *s.PaceSecPerUnit))
				break
			}
		}
	}

	// I7: HR-zone accounting — zone seconds fit inside the run's duration
	// (zones cover HR-carrying time only) and percentages sum to ~1.
	if zones != nil && zones.TotalHRSeconds > 0 {
		var zoneSec int
		var zonePct float64
		for _, z := range zones.Zones {
			zoneSec += z.TimeSeconds
			zonePct += z.TimePct
		}
		if zoneSec > a.DurationSeconds+invariantTimeTolSeconds {
			v = append(v, fmt.Sprintf("I7: zone seconds %d exceed duration %d", zoneSec, a.DurationSeconds))
		}
		if math.Abs(zonePct-1) > invariantPctTol {
			v = append(v, fmt.Sprintf("I7: zone percentages sum to %.3f", zonePct))
		}
	}

	return v
}
