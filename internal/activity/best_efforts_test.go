package activity

import (
	"math"
	"os"
	"regexp"
	"testing"
	"time"
)

// synthTrace builds a trackpoint stream from cumulative-distance / elapsed-
// second pairs anchored at a fixed base time. Distances are in meters,
// elapsed in seconds.
func synthTrace(base time.Time, dists []float64, elapsed []float64) []parsedTrackpoint {
	tps := make([]parsedTrackpoint, len(dists))
	for i := range dists {
		tps[i] = parsedTrackpoint{
			Time:           base.Add(time.Duration(elapsed[i] * float64(time.Second))),
			DistanceMeters: dists[i],
		}
	}
	return tps
}

// constantPaceTrace samples a constant-pace run of totalMeters over
// totalSeconds at the given sample spacing (in meters). The final sample
// lands exactly on totalMeters.
func constantPaceTrace(base time.Time, totalMeters, totalSeconds, sampleMeters float64) []parsedTrackpoint {
	var dists, elapsed []float64
	speed := totalMeters / totalSeconds // m/s
	for d := 0.0; d < totalMeters; d += sampleMeters {
		dists = append(dists, d)
		elapsed = append(elapsed, d/speed)
	}
	dists = append(dists, totalMeters)
	elapsed = append(elapsed, totalSeconds)
	return synthTrace(base, dists, elapsed)
}

func effortByKey(efforts []ActivityBestEffort, key string) (float64, bool) {
	for _, e := range efforts {
		if e.DistanceKey == key {
			return e.DurationSeconds, true
		}
	}
	return 0, false
}

// TestBestEfforts_ConstantPaceExactMultiple: a 5000 m / 1200 s constant-
// pace run sampled exactly at the target boundaries yields exactly 1200 s
// for 5K and the analytical time for 1mi.
func TestBestEfforts_ConstantPaceExactMultiple(t *testing.T) {
	base := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	// Sample every 100 m so both 5000 m and 1609.344 m windows are present.
	tps := constantPaceTrace(base, 5000, 1200, 100)

	efforts := bestEfforts(tps, StandardDistances)

	got5k, ok := effortByKey(efforts, "5k")
	if !ok {
		t.Fatal("missing 5k effort")
	}
	if math.Abs(got5k-1200) > 0.1 {
		t.Errorf("5k = %.4f, want ~1200", got5k)
	}

	// 1 mile at 1200/5000 s/m = 0.24 s/m → 1609.344 * 0.24 = 386.24256 s.
	want1mi := 1609.344 * (1200.0 / 5000.0)
	got1mi, ok := effortByKey(efforts, "1mi")
	if !ok {
		t.Fatal("missing 1mi effort")
	}
	if math.Abs(got1mi-want1mi) > 0.1 {
		t.Errorf("1mi = %.4f, want ~%.4f", got1mi, want1mi)
	}
}

// TestBestEfforts_NonExactSampling: same constant pace but sampled every
// ~7 m so 5000 m never lands on a boundary; right-edge interpolation must
// recover the analytical 1200 s to within ±0.1 s.
func TestBestEfforts_NonExactSampling(t *testing.T) {
	base := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	tps := constantPaceTrace(base, 5000, 1200, 7)

	efforts := bestEfforts(tps, StandardDistances)
	got5k, ok := effortByKey(efforts, "5k")
	if !ok {
		t.Fatal("missing 5k effort")
	}
	if math.Abs(got5k-1200) > 0.1 {
		t.Errorf("5k = %.4f, want 1200 ±0.1 (interpolation should remove boundary bias)", got5k)
	}
}

// TestBestEfforts_FastWindowInsideSlowRun is the load-bearing case: a
// 10-mile run at 9:00/mi with a 5K embedded at 7:00/mi. The 5K best effort
// must reflect the fast 7:00/mi segment, not the 9:00/mi overall average.
func TestBestEfforts_FastWindowInsideSlowRun(t *testing.T) {
	base := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)

	const mile = 1609.344
	slowSpeed := mile / (9 * 60.0) // 9:00/mi in m/s
	fastSpeed := mile / (7 * 60.0) // 7:00/mi in m/s

	// Build the trace segment by segment at 1 m resolution, embedding a
	// 5000 m fast block starting 2 miles in.
	total := 10 * mile
	fastStart := 2 * mile
	fastEnd := fastStart + 5000

	var dists, elapsed []float64
	d := 0.0
	tSec := 0.0
	step := 5.0
	for d <= total {
		dists = append(dists, d)
		elapsed = append(elapsed, tSec)
		speed := slowSpeed
		if d >= fastStart && d < fastEnd {
			speed = fastSpeed
		}
		tSec += step / speed
		d += step
	}

	efforts := bestEfforts(synthTrace(base, dists, elapsed), StandardDistances)
	got5k, ok := effortByKey(efforts, "5k")
	if !ok {
		t.Fatal("missing 5k effort")
	}

	want5kFast := 5000.0 / fastSpeed // ~1303.6 s (7:00/mi over 5K)
	want5kSlow := 5000.0 / slowSpeed // ~1676.5 s (9:00/mi over 5K)
	if math.Abs(got5k-want5kFast) > 5 {
		t.Errorf("5k = %.2f, want ~%.2f (the embedded 7:00/mi window), not the %.2f average",
			got5k, want5kFast, want5kSlow)
	}
}

// TestBestEfforts_TooShort: a 1.5-mile trace yields a 1mi entry but no
// 2mi/5k/10k/half/marathon entries.
func TestBestEfforts_TooShort(t *testing.T) {
	base := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	const mile = 1609.344
	tps := constantPaceTrace(base, 1.5*mile, 600, 10)

	efforts := bestEfforts(tps, StandardDistances)

	if _, ok := effortByKey(efforts, "1mi"); !ok {
		t.Error("expected a 1mi entry for a 1.5mi trace")
	}
	for _, k := range []string{"2mi", "5k", "10k", "half_marathon", "marathon"} {
		if _, ok := effortByKey(efforts, k); ok {
			t.Errorf("did not expect a %q entry for a 1.5mi trace", k)
		}
	}
}

// TestBestEfforts_MonotonicNonStrict: two consecutive trackpoints share a
// cumulative distance (a GPS glitch). The sweep must not divide by zero and
// must still produce a sensible 1mi result.
func TestBestEfforts_MonotonicNonStrict(t *testing.T) {
	base := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	const mile = 1609.344
	// 2000 m / 600 s constant pace, with a duplicate-distance pair injected
	// near the start.
	dists := []float64{0, 200, 200, 400, 800, 1200, 1609.344, 2000}
	speed := 2000.0 / 600.0
	elapsed := make([]float64, len(dists))
	for i, dm := range dists {
		elapsed[i] = dm / speed
	}
	// Give the duplicate-distance point a distinct (later) time so it's a
	// genuine non-strict monotonic step rather than a duplicate row.
	elapsed[2] = elapsed[1] + 1

	efforts := bestEfforts(synthTrace(base, dists, elapsed), StandardDistances)
	got1mi, ok := effortByKey(efforts, "1mi")
	if !ok {
		t.Fatal("missing 1mi effort")
	}
	if math.IsInf(got1mi, 0) || math.IsNaN(got1mi) {
		t.Fatalf("1mi = %v, want a finite value (no divide-by-zero)", got1mi)
	}
	// At ~3.33 m/s a mile takes ~482.8 s; allow generous slack for the glitch.
	wantApprox := mile / speed
	if math.Abs(got1mi-wantApprox) > 5 {
		t.Errorf("1mi = %.2f, want ~%.2f", got1mi, wantApprox)
	}
}

// TestBestEfforts_SparseAnchorsFindsTrueMinimum is a regression test for a
// bug where advancing the left pointer too aggressively skipped valid
// left-anchored windows. With sparse samples around the target boundary, an
// earlier anchor's interpolated right edge can yield a strictly faster
// window than any sample-aligned one — the sweep must not miss it.
//
// dist {0, 600, 1300, 1310, 2310}, time {0, 600, 700, 701, 900}, T = 1000:
// the window anchored at sample 1 (dist 600, t 600) crossing 1600 m
// (interpolated to t ≈ 758.7) gives ≈ 158.7 s, beating every other window.
func TestBestEfforts_SparseAnchorsFindsTrueMinimum(t *testing.T) {
	base := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	dists := []float64{0, 600, 1300, 1310, 2310}
	elapsed := []float64{0, 600, 700, 701, 900}
	tps := synthTrace(base, dists, elapsed)

	// Target exactly 1000 m via a synthetic single-distance sweep.
	target := StandardDistance{Key: "1000m", Meters: 1000}
	efforts := bestEfforts(tps, []StandardDistance{target})
	got, ok := effortByKey(efforts, "1000m")
	if !ok {
		t.Fatal("missing 1000m effort")
	}
	// Brute-force right-edge-interpolated minimum is ≈ 158.71 s.
	want := 158.7085
	if math.Abs(got-want) > 0.1 {
		t.Errorf("1000m window = %.4f, want ~%.4f (must not skip the sample-1 anchor)", got, want)
	}
}

// TestSummarize_NonRunningHasNoBestEfforts: a walk produces no best efforts.
func TestSummarize_NonRunningHasNoBestEfforts(t *testing.T) {
	base := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	tps := constantPaceTrace(base, 5000, 3600, 50)
	p := &parsedTCX{Trackpoints: tps}

	a := summarize(p, ActivityWalking)
	if len(a.BestEfforts) != 0 {
		t.Errorf("walk produced %d best efforts, want 0", len(a.BestEfforts))
	}
}

// TestStandardDistances_MatchMigrationCheck parses the 016 migration's
// CHECK(distance_key IN (...)) token set and asserts it equals the set of
// StandardDistances keys, so a typo in either place fails CI.
func TestStandardDistances_MatchMigrationCheck(t *testing.T) {
	data, err := os.ReadFile("../db/migrations/016_activity_best_efforts.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	re := regexp.MustCompile(`(?i)CHECK\(distance_key IN \(([^)]*)\)\)`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatal("could not find CHECK(distance_key IN (...)) in migration 016")
	}

	tokenRe := regexp.MustCompile(`'([^']+)'`)
	fromSQL := map[string]bool{}
	for _, tok := range tokenRe.FindAllSubmatch(m[1], -1) {
		fromSQL[string(tok[1])] = true
	}

	fromGo := map[string]bool{}
	for _, d := range StandardDistances {
		fromGo[d.Key] = true
	}

	if len(fromSQL) != len(fromGo) {
		t.Fatalf("key count mismatch: SQL=%v Go=%v", fromSQL, fromGo)
	}
	for k := range fromGo {
		if !fromSQL[k] {
			t.Errorf("key %q in StandardDistances but not in migration CHECK", k)
		}
	}
	for k := range fromSQL {
		if !fromGo[k] {
			t.Errorf("key %q in migration CHECK but not in StandardDistances", k)
		}
	}
}
