package activity

import (
	"math"
	"testing"
)

// tp builds a Trackpoint for derivation tests. Pace is per-km; nil = no sample.
func tp(seq, elapsed int, dist float64, pace *float64, hr *int, elev *float64) Trackpoint {
	return Trackpoint{Sequence: seq, ElapsedSeconds: elapsed, DistanceMeters: dist,
		PaceSecPerKm: pace, HeartRateBpm: hr, ElevationMeters: elev}
}

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

// steadyTrack builds an evenly-paced track: n+1 points, stepMeters apart,
// stepSeconds apart, all with a clean pace sample.
func steadyTrack(n int, stepMeters float64, stepSeconds int) []Trackpoint {
	tps := make([]Trackpoint, 0, n+1)
	for i := 0; i <= n; i++ {
		var pace *float64
		if i > 0 {
			pace = fp(float64(stepSeconds) / (stepMeters / 1000))
		}
		tps = append(tps, tp(i, i*stepSeconds, float64(i)*stepMeters, pace, nil, nil))
	}
	return tps
}

// TestDeriveRunning_SplitsReconcileByConstruction is the SOW's core promise:
// split distances sum to the stream span, split times sum to the duration,
// each split's pace IS its time over its distance, and the distance-weighted
// mean of split paces reproduces total time / total distance.
func TestDeriveRunning_SplitsReconcileByConstruction(t *testing.T) {
	// 5.3 "km" at 100 m / 30 s steps => 53 segments, 5300 m in 1590 s.
	tps := steadyTrack(53, 100, 30)
	d := deriveRunning(tps, UnitKm)

	if len(d.Splits) != 6 { // 5 full km + 300 m partial
		t.Fatalf("splits = %d, want 6", len(d.Splits))
	}
	var sumDist float64
	var sumTime int
	for _, s := range d.Splits {
		sumDist += s.DistanceMeters
		sumTime += s.DurationSeconds
		if s.PaceSecPerUnit == nil {
			t.Fatalf("split %d has nil pace", s.Index)
		}
		// I3: pace ≡ (time/dist) * bucket, exactly (one float op apart).
		want := float64(s.DurationSeconds) / s.DistanceMeters * 1000
		if math.Abs(*s.PaceSecPerUnit-want) > 0.5 {
			t.Errorf("split %d pace = %.2f, want %.2f", s.Index, *s.PaceSecPerUnit, want)
		}
	}
	if math.Abs(sumDist-5300) > 0.01 {
		t.Errorf("sum split dist = %.2f, want 5300", sumDist)
	}
	if sumTime != 1590 {
		t.Errorf("sum split time = %d, want 1590", sumTime)
	}
	last := d.Splits[len(d.Splits)-1]
	if !last.Partial {
		t.Error("trailing 300 m split should be partial")
	}
}

// TestDeriveRunning_DropoutSegmentsStayInSplitMath is the policy change: a
// mid-run stationary stretch (pace sample nil) still contributes its time, so
// the containing split's pace slows accordingly instead of pretending the
// stop never happened.
func TestDeriveRunning_DropoutSegmentsStayInSplitMath(t *testing.T) {
	// 1 km in 300 s, then 60 s stationary (no distance, nil pace), then 1 km in 300 s.
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, nil),
		tp(1, 300, 1000, fp(300), nil, nil),
		tp(2, 360, 1000, nil, nil, nil), // stationary: dDist=0, dTime=60
		tp(3, 660, 2000, fp(300), nil, nil),
	}
	d := deriveRunning(tps, UnitKm)
	if len(d.Splits) != 2 {
		t.Fatalf("splits = %d, want 2", len(d.Splits))
	}
	// The stationary segment starts at exactly 1000 m => bucket 1. Split 2
	// carries 1000 m in 360 s => pace 360 s/km, NOT 300.
	s2 := d.Splits[1]
	if s2.DurationSeconds != 360 {
		t.Errorf("split 2 time = %d, want 360", s2.DurationSeconds)
	}
	if s2.PaceSecPerUnit == nil || math.Abs(*s2.PaceSecPerUnit-360) > 0.5 {
		t.Errorf("split 2 pace = %v, want 360", s2.PaceSecPerUnit)
	}
	// Sum of split times still equals the full elapsed duration.
	if got := d.Splits[0].DurationSeconds + s2.DurationSeconds; got != 660 {
		t.Errorf("sum split time = %d, want 660", got)
	}
}

// TestDeriveRunning_MileBuckets checks the unit switch: same track, mile
// buckets, pace expressed per mile.
func TestDeriveRunning_MileBuckets(t *testing.T) {
	// 2 miles at 3218.688 m, steps of 160.9344 m / 60 s => 600 s per mile.
	tps := steadyTrack(20, 160.9344, 60)
	d := deriveRunning(tps, UnitMiles)
	if len(d.Splits) != 2 {
		t.Fatalf("splits = %d, want 2", len(d.Splits))
	}
	for _, s := range d.Splits {
		if s.PaceSecPerUnit == nil || math.Abs(*s.PaceSecPerUnit-600) > 0.5 {
			t.Errorf("split %d pace = %v, want 600 s/mi", s.Index, s.PaceSecPerUnit)
		}
	}
}

// TestDeriveRunning_FastestSlowestTags: tags only among full splits, only
// when at least two full splits exist.
func TestDeriveRunning_FastestSlowestTags(t *testing.T) {
	// km 1 in 360 s, km 2 in 300 s, 200 m tail in 80 s.
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, nil),
		tp(1, 360, 1000, fp(360), nil, nil),
		tp(2, 660, 2000, fp(300), nil, nil),
		tp(3, 740, 2200, fp(400), nil, nil),
	}
	d := deriveRunning(tps, UnitKm)
	if len(d.Splits) != 3 {
		t.Fatalf("splits = %d, want 3", len(d.Splits))
	}
	if !d.Splits[1].Fastest || d.Splits[0].Fastest {
		t.Error("km 2 should be tagged fastest")
	}
	if !d.Splits[0].Slowest || d.Splits[2].Slowest {
		t.Error("km 1 should be tagged slowest; partial never tagged")
	}

	// A single full split gets no tags.
	one := deriveRunning(steadyTrack(10, 100, 30), UnitKm) // 1 km exactly
	for _, s := range one.Splits {
		if s.Fastest || s.Slowest {
			t.Error("tags require >= 2 full splits")
		}
	}
}

// TestDeriveRunning_HRAndElevationPerSplit: split HR is the mean of
// segment-endpoint HRs; elevation delta is last-minus-first in the bucket.
func TestDeriveRunning_HRAndElevationPerSplit(t *testing.T) {
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, fp(100)),
		tp(1, 300, 500, fp(600), ip(130), fp(104)),
		tp(2, 600, 1000, fp(600), ip(150), fp(102)),
	}
	d := deriveRunning(tps, UnitKm)
	if len(d.Splits) != 1 {
		t.Fatalf("splits = %d, want 1", len(d.Splits))
	}
	s := d.Splits[0]
	if s.AvgHRBpm == nil || math.Abs(*s.AvgHRBpm-140) > 0.01 {
		t.Errorf("avg hr = %v, want 140", s.AvgHRBpm)
	}
	if s.ElevDeltaMeters == nil || math.Abs(*s.ElevDeltaMeters-(-2)) > 0.01 {
		t.Errorf("elev delta = %v, want -2 (104 -> 102 across segment endpoints)", s.ElevDeltaMeters)
	}
}

// TestDeriveRunning_Degenerate: empty and single-point tracks derive to
// nothing rather than panicking.
func TestDeriveRunning_Degenerate(t *testing.T) {
	for _, tps := range [][]Trackpoint{nil, {tp(0, 0, 0, nil, nil, nil)}} {
		d := deriveRunning(tps, UnitMiles)
		if len(d.Splits) != 0 {
			t.Errorf("degenerate track produced %d splits", len(d.Splits))
		}
	}
}

// TestBestRollingPace_MatchesWindowAndOrdering: the fastest rolling
// display-unit window is at most the fastest full split's pace (any aligned
// full bucket is itself a candidate window), and at least the fastest single
// clean sample.
func TestBestRollingPace_MatchesWindowAndOrdering(t *testing.T) {
	// km 1 at 360 s, km 2 at 300 s: fastest rolling km = the second km.
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, nil),
		tp(1, 180, 500, fp(360), nil, nil),
		tp(2, 360, 1000, fp(360), nil, nil),
		tp(3, 510, 1500, fp(300), nil, nil),
		tp(4, 660, 2000, fp(300), nil, nil),
	}
	best := bestRollingPace(tps, 1000)
	if best == nil || math.Abs(*best-300) > 0.5 {
		t.Fatalf("best rolling km = %v, want 300", best)
	}
	// Too short for the window => nil.
	if got := bestRollingPace(tps[:2], 1000); got != nil {
		t.Errorf("sub-window track best = %v, want nil", got)
	}
	if got := bestRollingPace(nil, 1000); got != nil {
		t.Errorf("nil track best = %v, want nil", got)
	}
}

// TestBuildStripSummary_CleanMinMaxAndDropoutCount: fastest/slowest are over
// clean samples only, expressed per display unit; dropout_count counts
// pace-carrying samples flagged non-clean; nil-pace points count for neither.
func TestBuildStripSummary_CleanMinMaxAndDropoutCount(t *testing.T) {
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, nil),           // no sample: not a dropout
		tp(1, 300, 1000, fp(300), nil, nil),  // clean
		tp(2, 900, 1500, fp(1200), nil, nil), // dropout (>410)
		tp(3, 1260, 2500, fp(360), nil, nil), // clean
	}
	s := buildStripSummary(tps, 1000)
	if s.FastestSecPerUnit == nil || math.Abs(*s.FastestSecPerUnit-300) > 0.01 {
		t.Errorf("fastest = %v, want 300", s.FastestSecPerUnit)
	}
	if s.SlowestSecPerUnit == nil || math.Abs(*s.SlowestSecPerUnit-360) > 0.01 {
		t.Errorf("slowest = %v, want 360", s.SlowestSecPerUnit)
	}
	if s.DropoutCount != 1 {
		t.Errorf("dropout_count = %d, want 1", s.DropoutCount)
	}

	// Mile unit converts the same sec/km values to sec/mi.
	mi := buildStripSummary(tps, metersPerMile)
	if mi.FastestSecPerUnit == nil || math.Abs(*mi.FastestSecPerUnit-300*1.609344) > 0.01 {
		t.Errorf("mi fastest = %v, want %.2f", mi.FastestSecPerUnit, 300*1.609344)
	}

	// No clean samples => nil min/max.
	none := buildStripSummary([]Trackpoint{tp(0, 0, 0, nil, nil, nil)}, 1000)
	if none.FastestSecPerUnit != nil || none.SlowestSecPerUnit != nil || none.DropoutCount != 0 {
		t.Errorf("empty strip summary = %+v", none)
	}
}

// intervalsTrack builds warmup + n×(work,recovery) + cooldown with clean
// samples throughout. Work pace is 240 s/km; recovery/warmup/cooldown pace is
// 400 s/km — deliberately under the 410 dropout threshold so every sample
// stays clean.
func intervalsTrack(nReps int) []Trackpoint {
	tps := []Trackpoint{tp(0, 0, 0, nil, nil, nil)}
	dist, elapsed, seq := 0.0, 0, 1
	add := func(meters float64, paceSecPerKm float64) {
		// 100 m steps so each bout is several segments long (survives denoise).
		steps := int(meters / 100)
		for i := 0; i < steps; i++ {
			dist += 100
			elapsed += int(paceSecPerKm * 0.1)
			p := paceSecPerKm
			tps = append(tps, tp(seq, elapsed, dist, &p, nil, nil))
			seq++
		}
	}
	add(800, 400) // warm-up
	for i := 0; i < nReps; i++ {
		add(400, 240) // work
		add(200, 400) // recovery
	}
	add(800, 400) // cool-down (the final recovery merges into it)
	return tps
}

// TestDetectIntervals_HappyPath: 4 reps detect with warmup/work/recovery/
// cooldown labels and per-bout pace = time/dist.
func TestDetectIntervals_HappyPath(t *testing.T) {
	segs := buildSegments(intervalsTrack(4), 1000)
	got := detectIntervals(segs, 1000)
	if got == nil {
		t.Fatal("expected detected intervals")
	}
	if got[0].Kind != "warmup" || got[0].Label != "Warm-up" {
		t.Errorf("first segment = %+v, want warm-up", got[0])
	}
	var reps int
	for _, s := range got {
		if s.Kind == "work" {
			reps++
			if s.Rep == nil || *s.Rep != reps {
				t.Errorf("work rep = %v, want %d", s.Rep, reps)
			}
			if s.PaceSecPerUnit == nil ||
				math.Abs(*s.PaceSecPerUnit-float64(s.DurationSeconds)/s.DistanceMeters*1000) > 0.5 {
				t.Errorf("work pace %v != time/dist", s.PaceSecPerUnit)
			}
		}
	}
	if reps != 4 {
		t.Errorf("work reps = %d, want 4", reps)
	}
	if last := got[len(got)-1]; last.Kind != "cooldown" {
		t.Errorf("last segment kind = %s, want cooldown", last.Kind)
	}
}

// TestDetectIntervals_ConservativeNil: steady runs and too-few-rep runs
// return nil rather than fabricating structure.
func TestDetectIntervals_ConservativeNil(t *testing.T) {
	if got := detectIntervals(buildSegments(steadyTrack(50, 100, 30), 1000), 1000); got != nil {
		t.Errorf("steady run detected intervals: %+v", got)
	}
	if got := detectIntervals(buildSegments(intervalsTrack(2), 1000), 1000); got != nil {
		t.Errorf("2-rep run detected intervals (needs >= 3): %+v", got)
	}
	if got := detectIntervals(nil, 1000); got != nil {
		t.Errorf("nil segments detected intervals: %+v", got)
	}
}

// TestDeriveRunning_NonMonotonicDistanceTelescopes: negative distance deltas
// are kept as-is, so split distances still sum exactly to last - first.
func TestDeriveRunning_NonMonotonicDistanceTelescopes(t *testing.T) {
	// Cumulative distance dips backwards mid-stream (GPS correction).
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, nil),
		tp(1, 100, 400, fp(250), nil, nil),
		tp(2, 200, 350, nil, nil, nil), // dips back 50 m
		tp(3, 300, 900, fp(250), nil, nil),
	}
	d := deriveRunning(tps, UnitKm)
	var sumDist float64
	for _, s := range d.Splits {
		sumDist += s.DistanceMeters
	}
	if want := 900.0 - 0.0; math.Abs(sumDist-want) > 0.001 {
		t.Errorf("split dist sum = %.3f, want %.3f (last - first)", sumDist, want)
	}
}

// TestDeriveRunning_BackwardsElapsedClamped: a backwards ElapsedSeconds pair
// contributes zero (not negative) time, so split durations stay >= 0.
func TestDeriveRunning_BackwardsElapsedClamped(t *testing.T) {
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, nil),
		tp(1, 100, 300, fp(333), nil, nil),
		tp(2, 50, 600, fp(333), nil, nil), // clock jumps backwards
	}
	d := deriveRunning(tps, UnitKm)
	if len(d.Splits) != 1 {
		t.Fatalf("splits = %d, want 1", len(d.Splits))
	}
	if got := d.Splits[0].DurationSeconds; got != 100 {
		t.Errorf("split time = %d, want 100 (backwards segment clamped to 0)", got)
	}
}

// TestDeriveRunning_StationaryTailPaceNil: a trailing bucket that covers no
// distance carries its time but has no pace to report.
func TestDeriveRunning_StationaryTailPaceNil(t *testing.T) {
	tps := []Trackpoint{
		tp(0, 0, 0, nil, nil, nil),
		tp(1, 300, 1000, fp(300), nil, nil),
		tp(2, 400, 1000, nil, nil, nil), // stops dead at exactly 1 km
		tp(3, 500, 1000, nil, nil, nil),
	}
	d := deriveRunning(tps, UnitKm)
	if len(d.Splits) != 2 {
		t.Fatalf("splits = %d, want 2", len(d.Splits))
	}
	tail := d.Splits[1]
	if tail.DistanceMeters != 0 || tail.DurationSeconds != 200 {
		t.Errorf("tail split = %+v, want 0 m in 200 s", tail)
	}
	if tail.PaceSecPerUnit != nil {
		t.Errorf("stationary tail pace = %v, want nil", *tail.PaceSecPerUnit)
	}
}
