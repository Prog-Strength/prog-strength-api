package running

import "math"

// maxTrackpoints is the target size of the downsampled track shipped to
// the chart. ~300 points keeps payloads small while preserving shape on a
// typical screen width.
const maxTrackpoints = 300

// summarize turns a validated parsedTCX into a Session (with its
// downsampled Trackpoints) ready for the caller to stamp with
// ID/UserID/TCXS3Key/timestamps. It assumes validate has already passed,
// so there is at least one trackpoint with non-zero cumulative distance;
// callers must not invoke it on an invalid file.
func summarize(p *parsedTCX) Session {
	tps := p.Trackpoints
	first := tps[0]
	last := tps[len(tps)-1]

	distance := last.DistanceMeters
	duration := int(last.Time.Sub(first.Time).Seconds())

	// The validator guarantees at least one non-zero-distance trackpoint, but
	// cumulative distance isn't guaranteed monotonic for hand-crafted input,
	// so guard the division rather than risk a +Inf/NaN pace.
	var avgPace float64
	if distance > 0 {
		avgPace = float64(duration) / (distance / 1000)
	}

	s := Session{
		GarminActivityID:    p.ActivityID,
		StartTime:           first.Time,
		Name:                p.Notes,
		DistanceMeters:      distance,
		DurationSeconds:     duration,
		AvgPaceSecPerKm:     avgPace,
		AvgHeartRateBpm:     avgHeartRate(tps),
		MaxHeartRateBpm:     maxHeartRate(tps),
		ElevationGainMeters: elevationGain(tps),
		BestPaceSecPerKm:    bestPace(tps),
	}

	if p.hasCalories {
		total := 0
		for _, c := range p.LapCalories {
			total += c
		}
		s.TotalCalories = &total
	}

	s.Trackpoints = downsample(tps, first)
	return s
}

// avgHeartRate is the rounded mean over only the trackpoints that carry
// HR. A run with a few dropped samples still gets a sensible average; a
// run with no strap at all returns nil.
func avgHeartRate(tps []parsedTrackpoint) *int {
	sum, n := 0, 0
	for _, tp := range tps {
		if tp.HeartRateBpm != nil {
			sum += *tp.HeartRateBpm
			n++
		}
	}
	if n == 0 {
		return nil
	}
	avg := int(math.Round(float64(sum) / float64(n)))
	return &avg
}

func maxHeartRate(tps []parsedTrackpoint) *int {
	var max *int
	for _, tp := range tps {
		if tp.HeartRateBpm == nil {
			continue
		}
		if max == nil || *tp.HeartRateBpm > *max {
			v := *tp.HeartRateBpm
			max = &v
		}
	}
	return max
}

// elevationGain sums only the positive consecutive altitude deltas (total
// ascent). Returns nil when no trackpoint had altitude at all — distinct
// from a flat run, which legitimately gains 0.
func elevationGain(tps []parsedTrackpoint) *float64 {
	var prev *float64
	gain := 0.0
	any := false
	for _, tp := range tps {
		if tp.AltitudeMeters == nil {
			continue
		}
		any = true
		if prev != nil {
			if d := *tp.AltitudeMeters - *prev; d > 0 {
				gain += d
			}
		}
		prev = tp.AltitudeMeters
	}
	if !any {
		return nil
	}
	return &gain
}

// bestPace finds the fastest 1-km split using a distance-anchored sliding
// window. We expand the right edge until at least 1000 m separates it from
// the left edge, then the window's pace is its elapsed time over exactly
// 1 km. Anchoring on distance (not sample count) makes the result robust
// to GPS jitter: a single noisy sample (e.g. a 50 m teleport in 1 s) is
// diluted across a full kilometre, so it can't poison the minimum the way
// an instantaneous per-sample pace would. Returns nil when run is < 1 km.
func bestPace(tps []parsedTrackpoint) *float64 {
	if len(tps) == 0 || tps[len(tps)-1].DistanceMeters-tps[0].DistanceMeters < 1000 {
		return nil
	}
	best := math.Inf(1)
	left := 0
	for right := 0; right < len(tps); right++ {
		// Advance left while the window can shrink and still span >= 1 km,
		// so each right edge pairs with the tightest qualifying window (the
		// fastest candidate ending at that point). We stop one short of the
		// boundary: moving left further would drop below 1 km.
		for left+1 < right && tps[right].DistanceMeters-tps[left+1].DistanceMeters >= 1000 {
			left++
		}
		if tps[right].DistanceMeters-tps[left].DistanceMeters >= 1000 {
			elapsed := tps[right].Time.Sub(tps[left].Time).Seconds()
			// Normalize by the window's actual span (>= 1 km), not a flat
			// 1.0, so a window slightly over 1 km isn't reported as
			// artificially slow.
			km := (tps[right].DistanceMeters - tps[left].DistanceMeters) / 1000
			if pace := elapsed / km; pace < best {
				best = pace
			}
		}
	}
	if math.IsInf(best, 1) {
		return nil
	}
	return &best
}

// downsample reduces the raw track to ~maxTrackpoints evenly strided
// points, always keeping the first and last so the chart's endpoints are
// exact. Per-kept-point pace is computed between consecutive KEPT points
// (not raw neighbours), matching what the chart actually draws.
func downsample(raw []parsedTrackpoint, first parsedTrackpoint) []Trackpoint {
	// summarize only calls this on a validated (non-empty) track, but guard
	// anyway so the index math below can't panic on a degenerate input.
	if len(raw) == 0 {
		return []Trackpoint{}
	}
	stride := len(raw) / maxTrackpoints
	if stride < 1 {
		stride = 1
	}

	// Collect indices on the stride, then guarantee the final raw point.
	var idx []int
	for i := 0; i < len(raw); i += stride {
		idx = append(idx, i)
	}
	if idx[len(idx)-1] != len(raw)-1 {
		idx = append(idx, len(raw)-1)
	}

	out := make([]Trackpoint, 0, len(idx))
	var prev *parsedTrackpoint
	for seq, i := range idx {
		rp := raw[i]
		tp := Trackpoint{
			Sequence:        seq,
			ElapsedSeconds:  int(rp.Time.Sub(first.Time).Seconds()),
			DistanceMeters:  rp.DistanceMeters,
			HeartRateBpm:    rp.HeartRateBpm,
			ElevationMeters: rp.AltitudeMeters,
		}
		// Pace is nil for the first kept point (no prior segment) and when
		// two kept points share a distance (would divide by zero).
		if prev != nil {
			if dKm := (rp.DistanceMeters - prev.DistanceMeters) / 1000; dKm > 0 {
				pace := rp.Time.Sub(prev.Time).Seconds() / dKm
				tp.PaceSecPerKm = &pace
			}
		}
		out = append(out, tp)
		p := rp
		prev = &p
	}
	return out
}
