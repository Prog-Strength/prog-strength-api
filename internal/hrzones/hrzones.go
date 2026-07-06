package hrzones

import (
	"math"
	"sort"
)

// Config holds the tunables for the zone model and reference estimation.
type Config struct {
	PopulationDefaultMaxHR int
	CalibratedRunThreshold int
	RecencyWindowDays      int
	MinReferenceBpm        int
	MaxReferenceBpm        int
	ZoneUpperBounds        []float64 // ascending interior bounds, e.g. [0.60,0.70,0.80,0.90]
	ZoneNames              []string  // len == len(ZoneUpperBounds)+1
}

// Stats is the historical HR summary a caller assembles for a user.
type Stats struct {
	RecentHRSamplesP99 *int
	HistoryRunCount    int
	CurrentRunP99      *int
}

// Confidence describes how trustworthy a reference max-HR is.
type Confidence string

const (
	ConfidenceEstimated   Confidence = "estimated"
	ConfidenceCalibrating Confidence = "calibrating"
	ConfidenceCalibrated  Confidence = "calibrated"
)

// Reference is the resolved max-HR used to derive zone boundaries.
type Reference struct {
	MaxHRBpm   int
	Source     string // "population_default" | "current_run" | "p99_recent_runs"
	Confidence Confidence
}

// Trackpoint is a single sample along an activity.
type Trackpoint struct {
	ElapsedSeconds int
	HeartRateBpm   *int
}

// Zone is one band of the five-zone model plus its accumulated time.
type Zone struct {
	Number      int
	Name        string
	LowerPct    float64
	UpperPct    float64
	MinBpm      int
	MaxBpm      int
	TimeSeconds int
	TimePct     float64
}

// Result is the full time-in-zone breakdown for an activity.
type Result struct {
	Model          string // "percent_max_hr"
	Reference      Reference
	TotalHRSeconds int
	Zones          []Zone
	Calibrating    bool
}

// Engine computes zones and references from a fixed Config.
type Engine struct{ cfg Config }

// New returns an Engine bound to cfg.
func New(cfg Config) *Engine { return &Engine{cfg: cfg} }

// lowerPct returns the inclusive lower fraction of zone i (0-indexed).
// Zone 0 starts at 0.0; every other zone starts at the previous interior bound.
func (e *Engine) lowerPct(i int) float64 {
	if i == 0 {
		return 0.0
	}
	return e.cfg.ZoneUpperBounds[i-1]
}

// upperPct returns the exclusive upper fraction of zone i (0-indexed).
// The top zone is the open-ended ceiling, reported as 1.0.
func (e *Engine) upperPct(i int) float64 {
	if i == len(e.cfg.ZoneUpperBounds) {
		return 1.0
	}
	return e.cfg.ZoneUpperBounds[i]
}

// buildZones materializes the static zone fields (no time) for a reference.
func (e *Engine) buildZones(maxHR int) []Zone {
	n := len(e.cfg.ZoneUpperBounds) + 1
	zones := make([]Zone, n)
	for i := 0; i < n; i++ {
		lower := e.lowerPct(i)
		upper := e.upperPct(i)

		// Bounds must derive from the SAME threshold classify uses: the lower
		// zone contains every integer v < upper*maxHR. So MinBpm is the first
		// integer at/above lower*maxHR (ceil), and MaxBpm is one below the first
		// integer at/above upper*maxHR (ceil - 1). Using Round here would let the
		// displayed range contradict where classify counts time whenever
		// upper*maxHR has a fractional part below 0.5.
		minBpm := int(math.Ceil(lower * float64(maxHR)))
		var maxBpm int
		if i == n-1 {
			// Top zone is open-ended; its ceiling is the reference itself.
			maxBpm = maxHR
		} else {
			maxBpm = int(math.Ceil(upper*float64(maxHR))) - 1
		}

		zones[i] = Zone{
			Number:   i + 1,
			Name:     e.cfg.ZoneNames[i],
			LowerPct: lower,
			UpperPct: upper,
			MinBpm:   minBpm,
			MaxBpm:   maxBpm,
		}
	}
	return zones
}

// classify returns the 0-indexed zone for a bpm value at the given reference.
// We compare against the fractional threshold (float64(value) < upper*maxHR)
// rather than the rounded MaxBpm so boundary math is done once and stays
// consistent with buildZones; the top zone is the catch-all fallthrough.
func (e *Engine) classify(value float64, maxHR int) int {
	for i := 0; i < len(e.cfg.ZoneUpperBounds); i++ {
		if value < e.cfg.ZoneUpperBounds[i]*float64(maxHR) {
			return i
		}
	}
	return len(e.cfg.ZoneUpperBounds)
}

// ZoneForBPM returns the 0-indexed zone for bpm at ref. Exported for callers
// that need single-sample classification (e.g. max-effort window enrichment).
func (e *Engine) ZoneForBPM(ref Reference, bpm int) int {
	return e.classify(float64(bpm), ref.MaxHRBpm)
}

// EstimateReference resolves a max-HR reference along a cold-start -> calibrated
// ladder. As HR history accumulates the source shifts from a population default
// to the run's own p99 and finally to the p99 over recent runs, with confidence
// rising in step. The final value is clamped to the configured plausible band
// without altering the chosen source or confidence.
func (e *Engine) EstimateReference(s Stats) Reference {
	var ref Reference

	switch {
	case s.HistoryRunCount >= e.cfg.CalibratedRunThreshold && s.RecentHRSamplesP99 != nil:
		ref = Reference{
			MaxHRBpm:   *s.RecentHRSamplesP99,
			Source:     "p99_recent_runs",
			Confidence: ConfidenceCalibrated,
		}
	case s.HistoryRunCount > 0 && s.RecentHRSamplesP99 != nil:
		currentOr0 := 0
		if s.CurrentRunP99 != nil {
			currentOr0 = *s.CurrentRunP99
		}
		ref = Reference{
			MaxHRBpm:   max(*s.RecentHRSamplesP99, currentOr0),
			Source:     "p99_recent_runs",
			Confidence: ConfidenceCalibrating,
		}
	default:
		ref = Reference{
			MaxHRBpm:   e.cfg.PopulationDefaultMaxHR,
			Source:     "population_default",
			Confidence: ConfidenceEstimated,
		}
		if s.CurrentRunP99 != nil && *s.CurrentRunP99 > e.cfg.PopulationDefaultMaxHR {
			ref.MaxHRBpm = *s.CurrentRunP99
			ref.Source = "current_run"
		}
	}

	ref.MaxHRBpm = clamp(ref.MaxHRBpm, e.cfg.MinReferenceBpm, e.cfg.MaxReferenceBpm)
	return ref
}

// clamp bounds v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Compute accumulates time-in-zone over consecutive trackpoint pairs.
//
// A pair contributes only when both endpoints carry HR and dt > 0; the pair's
// zone is chosen by the mean of the two endpoint bpms (a float, classified
// against the fractional thresholds). Returns ok=false when no usable HR
// interval exists.
func (e *Engine) Compute(ref Reference, tps []Trackpoint) (Result, bool) {
	zones := e.buildZones(ref.MaxHRBpm)

	total := 0
	for i := 1; i < len(tps); i++ {
		prev, cur := tps[i-1], tps[i]
		if prev.HeartRateBpm == nil || cur.HeartRateBpm == nil {
			continue
		}
		dt := cur.ElapsedSeconds - prev.ElapsedSeconds
		if dt <= 0 {
			continue
		}
		mean := (float64(*prev.HeartRateBpm) + float64(*cur.HeartRateBpm)) / 2.0
		z := e.classify(mean, ref.MaxHRBpm)
		zones[z].TimeSeconds += dt
		total += dt
	}

	if total == 0 {
		return Result{}, false
	}

	for i := range zones {
		zones[i].TimePct = float64(zones[i].TimeSeconds) / float64(total)
	}

	return Result{
		Model:          "percent_max_hr",
		Reference:      ref,
		TotalHRSeconds: total,
		Zones:          zones,
		Calibrating:    ref.Confidence != ConfidenceCalibrated,
	}, true
}

// P99 returns the 99th-percentile of samples by nearest-rank, nil if empty.
// Nearest-rank is deliberately spike-resistant: a lone outlier sample cannot
// pull the reference up the way a plain max would.
func P99(samples []int) *int {
	if len(samples) == 0 {
		return nil
	}
	sorted := make([]int, len(samples))
	copy(sorted, samples)
	sort.Ints(sorted)

	rank := int(math.Ceil(0.99 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	v := sorted[rank-1]
	return &v
}
