package activity

import "math"

// maxTrackpoints is the target size of the downsampled track shipped to
// the chart. ~300 points keeps payloads small while preserving shape on a
// typical screen width.
const maxTrackpoints = 300

// paceFilterMinSpeedMps is the lower bound on instantaneous speed that
// produces a real pace value on a downsampled trackpoint. Segments slower
// than this are treated as stationary (warmup standing, GPS-fix wandering
// before the start) and their pace is nulled out so the chart doesn't
// render a multi-thousand-s/km spike that flattens the rest of the line.
//
// 0.5 m/s is ~33:20 min/km / ~53:36 min/mile — well below any plausible
// walking pace (a 30 min/mile walker still moves at ~0.9 m/s), so the
// filter only catches genuinely stationary periods, not slow walking
// recoveries during interval workouts.
const paceFilterMinSpeedMps = 0.5

// summarize turns a validated parsedTCX into an Activity (with its
// downsampled Trackpoints) ready for the caller to stamp with
// ID/UserID/IngestSource/TCXS3Key/timestamps. It assumes validate has
// already passed, so there is at least one trackpoint with non-zero
// cumulative distance; callers must not invoke it on an invalid file.
//
// actType drives which summary fields are populated:
//   - Pace fields (avg, best) are computed only for ActivityRunning.
//     Pace is a running-display concept; surfacing a "fastest 1km split"
//     on a cycling ride or walk would be misleading at the UI layer.
//   - HR, calories, elevation are computed when the source data carries
//     them, regardless of sport — they're meaningful for any activity.
func summarize(p *parsedTCX, actType ActivityType) Activity {
	tps := p.Trackpoints
	first := tps[0]
	last := tps[len(tps)-1]

	distance := last.DistanceMeters
	duration := int(last.Time.Sub(first.Time).Seconds())

	a := Activity{
		SourceActivityID:    p.ActivityID,
		ActivityType:        actType,
		StartTime:           first.Time,
		Name:                p.Notes,
		DistanceMeters:      distance,
		RawDistanceMeters:   distance,
		Environment:         EnvironmentOutdoor,
		DurationSeconds:     duration,
		AvgHeartRateBpm:     avgHeartRate(tps),
		MaxHeartRateBpm:     maxHeartRate(tps),
		ElevationGainMeters: elevationGain(tps),
	}

	if actType == ActivityRunning {
		// A running activity with no <Position> anywhere was recorded without
		// GPS — treadmill/indoor. Non-running sports keep the outdoor default;
		// only running is auto-tagged here.
		if !p.HasPosition {
			a.Environment = EnvironmentIndoor
		}
		// Guard the division rather than risk a +Inf/NaN pace: validate
		// guarantees at least one non-zero-distance trackpoint, but the
		// cumulative distance axis isn't guaranteed monotonic for
		// hand-crafted input.
		if distance > 0 {
			avg := float64(duration) / (distance / 1000)
			a.AvgPaceSecPerKm = &avg
		}
		a.BestPaceSecPerKm = bestPace(tps)
		// Best efforts (running PRs) are an outdoor-only surface. Indoor runs
		// are excluded by design: no activity_best_efforts rows are written,
		// so they never appear in PRs or feed max-effort estimates.
		if a.Environment == EnvironmentOutdoor {
			a.BestEfforts = bestEfforts(tps, StandardDistances)
		}
	}

	if p.hasCalories {
		total := 0
		for _, c := range p.LapCalories {
			total += c
		}
		a.TotalCalories = &total
	}

	a.Trackpoints = downsample(tps, first, actType)
	return a
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
	var hi *int
	for _, tp := range tps {
		if tp.HeartRateBpm == nil {
			continue
		}
		if hi == nil || *tp.HeartRateBpm > *hi {
			v := *tp.HeartRateBpm
			hi = &v
		}
	}
	return hi
}

// elevationGain sums only the positive consecutive altitude deltas (total
// ascent). Returns nil when no trackpoint had altitude at all — distinct
// from a flat activity, which legitimately gains 0.
func elevationGain(tps []parsedTrackpoint) *float64 {
	var prev *float64
	gain := 0.0
	seen := false
	for _, tp := range tps {
		if tp.AltitudeMeters == nil {
			continue
		}
		seen = true
		if prev != nil {
			if d := *tp.AltitudeMeters - *prev; d > 0 {
				gain += d
			}
		}
		prev = tp.AltitudeMeters
	}
	if !seen {
		return nil
	}
	return &gain
}

// bestPace finds the fastest 1-km split using a distance-anchored sliding
// window. We expand the right edge until at least 1000 m separates it from
// the left edge, then the window's pace is its elapsed time over exactly
// 1 km. Anchoring on distance (not sample count) makes the result robust
// to GPS jitter: a single noisy sample (e.g. a 50 m teleport in 1 s) is
// diluted across a full kilometer, so it can't poison the minimum the way
// an instantaneous per-sample pace would. Returns nil when the activity
// is < 1 km. Only meaningful for running; callers gate accordingly.
func bestPace(tps []parsedTrackpoint) *float64 {
	if len(tps) == 0 || tps[len(tps)-1].DistanceMeters-tps[0].DistanceMeters < 1000 {
		return nil
	}
	best := math.Inf(1)
	left := 0
	for right := 0; right < len(tps); right++ {
		for left+1 < right && tps[right].DistanceMeters-tps[left+1].DistanceMeters >= 1000 {
			left++
		}
		if tps[right].DistanceMeters-tps[left].DistanceMeters >= 1000 {
			elapsed := tps[right].Time.Sub(tps[left].Time).Seconds()
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
// (not raw neighbors) for running activities, matching what the chart
// draws. Non-running activities skip the per-point pace computation.
func downsample(raw []parsedTrackpoint, first parsedTrackpoint, actType ActivityType) []Trackpoint {
	// summarize only calls this on a validated (non-empty) track, but guard
	// anyway so the index math below can't panic on a degenerate input.
	if len(raw) == 0 {
		return []Trackpoint{}
	}
	stride := len(raw) / maxTrackpoints
	if stride < 1 {
		stride = 1
	}

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
		// Pace is nil for the first kept point (no prior segment), when
		// two kept points share a distance (would divide by zero), and
		// when the segment's instantaneous speed falls below the
		// stationary-filter threshold (see paceFilterMinSpeedMps). Also
		// nil for non-running activities — pace is a running display
		// concept.
		if actType == ActivityRunning && prev != nil {
			dMeters := rp.DistanceMeters - prev.DistanceMeters
			dSeconds := rp.Time.Sub(prev.Time).Seconds()
			if dMeters > 0 && dSeconds > 0 && dMeters/dSeconds >= paceFilterMinSpeedMps {
				pace := dSeconds / (dMeters / 1000)
				tp.PaceSecPerKm = &pace
			}
		}
		out = append(out, tp)
		p := rp
		prev = &p
	}
	return out
}
