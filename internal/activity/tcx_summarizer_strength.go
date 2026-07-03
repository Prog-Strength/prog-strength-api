package activity

import "time"

// summarizeStrength turns a parsed TCX into a strength-training Activity:
// the heart-rate / effort layer of a Garmin "Strength Training" session.
// It is the run summarizer with every distance-dependent computation
// removed — a strength session is stationary, so distance, pace, elevation,
// and best efforts are meaningless and left at zero/nil/empty.
//
// The caller stamps ID/UserID/IngestSource/TCXS3Key/timestamps afterward;
// ActivityType is fixed to strength_training here regardless of the file's
// <Sport> tag. Attaching a running TCX to a strength workout therefore keeps
// only its HR/calories — its distance and pace are intentionally discarded
// (if the user wanted a run, the run importer is the right entrypoint).
//
// Unlike summarize, this does not assume a non-empty track: validateStrength
// only guarantees HR-or-calories, not trackpoints, so every access is guarded.
func summarizeStrength(p *parsedTCX) Activity {
	tps := p.Trackpoints

	a := Activity{
		SourceActivityID: p.ActivityID,
		ActivityType:     ActivityStrengthTraining,
		StartTime:        strengthStartTime(p),
		Name:             p.Notes,
		DistanceMeters:   0,
		// A strength session is stationary, so it's neither indoor nor outdoor
		// in the treadmill sense; the environment column is general and NOT
		// NULL, so default it to outdoor (the same default the migration gives
		// every pre-existing row). RawDistanceMeters stays 0, matching the
		// zero distance.
		Environment:     EnvironmentOutdoor,
		AvgHeartRateBpm: avgHeartRate(tps),
		MaxHeartRateBpm: maxHeartRate(tps),
		// Distance-free: paces, elevation, and best efforts stay nil/empty.
	}

	if len(tps) > 0 {
		a.DurationSeconds = int(tps[len(tps)-1].Time.Sub(tps[0].Time).Seconds())
	}

	if p.hasCalories {
		total := 0
		for _, c := range p.LapCalories {
			total += c
		}
		a.TotalCalories = &total
	}

	a.Trackpoints = downsampleStrength(tps)
	return a
}

// strengthStartTime is the session's start: the first trackpoint's timestamp,
// falling back to the first lap's StartTime when a file carries laps but no
// trackpoints. Zero time if neither is present (a degenerate file that
// validateStrength only admits when it has calories).
func strengthStartTime(p *parsedTCX) time.Time {
	if len(p.Trackpoints) > 0 {
		return p.Trackpoints[0].Time
	}
	if len(p.LapStartTimes) > 0 {
		return p.LapStartTimes[0]
	}
	return time.Time{}
}

// downsampleStrength reduces the raw track to ~maxTrackpoints evenly-strided
// points carrying only elapsed seconds and heart rate — the two signals the
// heart-rate chart draws. Distance is forced to 0 and pace/elevation to nil
// even if the source file carried them (a misattached running TCX), so a
// strength activity never leaks distance into its trackpoints. The stride
// logic is shared with the run downsampler: keep the first and last point so
// the chart's endpoints are exact.
func downsampleStrength(raw []parsedTrackpoint) []Trackpoint {
	if len(raw) == 0 {
		return []Trackpoint{}
	}
	first := raw[0]
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
	for seq, i := range idx {
		rp := raw[i]
		out = append(out, Trackpoint{
			Sequence:       seq,
			ElapsedSeconds: int(rp.Time.Sub(first.Time).Seconds()),
			DistanceMeters: 0,
			HeartRateBpm:   rp.HeartRateBpm,
		})
	}
	return out
}
