package activity

import "math"

// This file is the read-time derivation for the running detail response: the
// single computation every rendered number on /running/[id] traces back to.
// Policy (SOW running-detail-metric-alignment): a split's pace is its TOTAL
// time over its TOTAL distance — dropout/stationary segments are not excluded
// from split math (they remain a chart-rendering concern via clean-pace
// flags), so split rows, the duration/distance tiles, and the avg-pace tile
// reconcile by construction. checkDetailInvariants (invariants.go) verifies
// exactly that on every assembled response.

// DistanceUnit selects the split-bucket length (and pace denominator) for the
// read-time derivation. The client passes ?unit=; splits are inherently
// unit-shaped (mile rows vs km rows), so this cannot be a render-side concern.
type DistanceUnit string

const (
	UnitMiles DistanceUnit = "mi"
	UnitKm    DistanceUnit = "km"
)

const metersPerMile = 1609.344

func (u DistanceUnit) Valid() bool { return u == UnitMiles || u == UnitKm }

// BucketMeters is one display unit expressed in meters.
func (u DistanceUnit) BucketMeters() float64 {
	if u == UnitMiles {
		return metersPerMile
	}
	return 1000
}

// paceDropoutSecPerKm mirrors the web's former PACE_DROPOUT_SEC_PER_KM: a
// per-point pace sample slower than this is treated as a device dropout —
// flagged (clean_pace=false) so the chart renders a gap, and excluded from
// the strip summary's fastest/slowest. It no longer excludes anything from
// split or summary arithmetic.
const paceDropoutSecPerKm = 410.0

// isCleanTrackpointPace reports whether a per-point pace sample is plottable:
// present, positive, and not slower than the dropout threshold.
func isCleanTrackpointPace(paceSecPerKm *float64) bool {
	return paceSecPerKm != nil && *paceSecPerKm > 0 && *paceSecPerKm <= paceDropoutSecPerKm
}

// Split is one distance bucket of the run. PaceSecPerUnit is nil only when
// the bucket covered no distance (pure stationary tail).
type Split struct {
	Index           int
	Partial         bool
	DistanceMeters  float64
	DurationSeconds int
	PaceSecPerUnit  *float64
	AvgHRBpm        *float64
	ElevDeltaMeters *float64
	Fastest         bool
	Slowest         bool
}

// StripSummary carries the pace-chart header numbers: min/max over CLEAN
// samples (the header describes the drawn line, which has gaps) and how many
// pace-carrying samples were flagged as dropouts.
type StripSummary struct {
	FastestSecPerUnit *float64
	SlowestSecPerUnit *float64
	DropoutCount      int
}

// IntervalSegment is one labeled bout of a detected interval workout.
type IntervalSegment struct {
	Kind            string // "warmup" | "work" | "recovery" | "cooldown"
	Rep             *int
	Label           string
	DistanceMeters  float64
	DurationSeconds int
	PaceSecPerUnit  *float64
	AvgHRBpm        *float64
}

// Derivation is everything the detail page renders beyond the raw summary
// fields, computed in one pass from the stored trackpoints.
type Derivation struct {
	Splits             []Split
	StripSummary       StripSummary
	BestPaceSecPerUnit *float64
	// Intervals is nil unless the workout confidently looks like intervals;
	// the client additionally gates display on the linked plan's run type.
	Intervals []IntervalSegment
}

// segment is one consecutive-pair slice of the track. dDist is taken as-is
// (a non-monotonic cumulative stream yields <= 0) so bucket distances
// telescope exactly to last-minus-first; dTime is clamped at 0.
type segment struct {
	dDist  float64
	dTime  int
	clean  bool
	bucket int
	hr     *int
	elev   *float64
}

func buildSegments(tps []Trackpoint, bucketMeters float64) []segment {
	if len(tps) < 2 {
		return nil
	}
	segs := make([]segment, 0, len(tps)-1)
	for i := 1; i < len(tps); i++ {
		a, b := tps[i-1], tps[i]
		dTime := b.ElapsedSeconds - a.ElapsedSeconds
		if dTime < 0 {
			dTime = 0
		}
		segs = append(segs, segment{
			dDist:  b.DistanceMeters - a.DistanceMeters,
			dTime:  dTime,
			clean:  isCleanTrackpointPace(b.PaceSecPerKm),
			bucket: int(math.Floor(a.DistanceMeters / bucketMeters)),
			hr:     b.HeartRateBpm,
			elev:   b.ElevationMeters,
		})
	}
	return segs
}

// deriveRunning computes the full detail derivation for a running activity.
func deriveRunning(tps []Trackpoint, unit DistanceUnit) Derivation {
	bucketMeters := unit.BucketMeters()
	segs := buildSegments(tps, bucketMeters)
	return Derivation{
		Splits:             buildDerivedSplits(segs, bucketMeters),
		StripSummary:       buildStripSummary(tps, bucketMeters),
		BestPaceSecPerUnit: bestRollingPace(tps, bucketMeters),
		Intervals:          detectIntervals(segs, bucketMeters),
	}
}

// splitAcc folds segments into one bucket.
type splitAcc struct {
	dist      float64
	timeSec   int
	hrSum     float64
	hrCount   int
	firstElev *float64
	lastElev  *float64
}

func buildDerivedSplits(segs []segment, bucketMeters float64) []Split {
	if len(segs) == 0 {
		return nil
	}
	byBucket := map[int]*splitAcc{}
	var order []int
	for _, s := range segs {
		acc, ok := byBucket[s.bucket]
		if !ok {
			acc = &splitAcc{}
			byBucket[s.bucket] = acc
			order = append(order, s.bucket)
		}
		acc.dist += s.dDist
		acc.timeSec += s.dTime
		if s.hr != nil {
			acc.hrSum += float64(*s.hr)
			acc.hrCount++
		}
		if s.elev != nil {
			if acc.firstElev == nil {
				acc.firstElev = s.elev
			}
			acc.lastElev = s.elev
		}
	}
	// Buckets appear in stream order, which is ascending for any monotonic
	// track; sort anyway so a jittery stream can't reorder rows.
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && order[j] < order[j-1]; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	splits := make([]Split, 0, len(order))
	for i, b := range order {
		acc := byBucket[b]
		sp := Split{
			Index:           i,
			DistanceMeters:  acc.dist,
			DurationSeconds: acc.timeSec,
		}
		if acc.dist > 0 {
			// THE policy line: pace is total time over total distance.
			pace := float64(acc.timeSec) / acc.dist * bucketMeters
			sp.PaceSecPerUnit = &pace
		}
		if acc.hrCount > 0 {
			avg := acc.hrSum / float64(acc.hrCount)
			sp.AvgHRBpm = &avg
		}
		if acc.firstElev != nil && acc.lastElev != nil {
			d := *acc.lastElev - *acc.firstElev
			sp.ElevDeltaMeters = &d
		}
		splits = append(splits, sp)
	}
	if n := len(splits); n > 0 && splits[n-1].DistanceMeters < bucketMeters*0.95 {
		splits[n-1].Partial = true
	}
	// Fastest/slowest among FULL splits with a pace — only when >= 2 exist.
	var fastest, slowest *Split
	full := 0
	for i := range splits {
		s := &splits[i]
		if s.Partial || s.PaceSecPerUnit == nil {
			continue
		}
		full++
		if fastest == nil || *s.PaceSecPerUnit < *fastest.PaceSecPerUnit {
			fastest = s
		}
		if slowest == nil || *s.PaceSecPerUnit > *slowest.PaceSecPerUnit {
			slowest = s
		}
	}
	if full >= 2 {
		fastest.Fastest = true
		slowest.Slowest = true
	}
	return splits
}

func buildStripSummary(tps []Trackpoint, bucketMeters float64) StripSummary  { return StripSummary{} }
func bestRollingPace(tps []Trackpoint, windowMeters float64) *float64        { return nil }
func detectIntervals(segs []segment, bucketMeters float64) []IntervalSegment { return nil }
