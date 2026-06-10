package activity

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSummarize_Typical5k(t *testing.T) {
	p, err := parseTCX(readFixture(t, "typical_5k.tcx"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := summarize(p, ActivityRunning)

	// Distance: 5000 m exactly by construction (±10 m tolerance).
	if math.Abs(a.DistanceMeters-5000) > 10 {
		t.Errorf("DistanceMeters = %.2f, want ~5000", a.DistanceMeters)
	}
	// 600 points at 1 Hz => 599 s span (±1 s).
	if a.DurationSeconds < 598 || a.DurationSeconds > 600 {
		t.Errorf("DurationSeconds = %d, want ~599", a.DurationSeconds)
	}
	// HR alternates 140/160 => mean exactly 150.
	if a.AvgHeartRateBpm == nil || *a.AvgHeartRateBpm != 150 {
		t.Errorf("AvgHeartRateBpm = %v, want 150", a.AvgHeartRateBpm)
	}
	if a.MaxHeartRateBpm == nil || *a.MaxHeartRateBpm != 160 {
		t.Errorf("MaxHeartRateBpm = %v, want 160", a.MaxHeartRateBpm)
	}
	// Calories summed from the single lap.
	if a.TotalCalories == nil || *a.TotalCalories != 350 {
		t.Errorf("TotalCalories = %v, want 350", a.TotalCalories)
	}
	// Altitude climbs 100->150 (gain 50) then descends; gain ~50 m (±1).
	if a.ElevationGainMeters == nil || math.Abs(*a.ElevationGainMeters-50) > 1 {
		t.Errorf("ElevationGainMeters = %v, want ~50", a.ElevationGainMeters)
	}
	// Avg pace = 599 s / 5 km ~= 119.8 s/km. For running, AvgPaceSecPerKm
	// is populated; for non-running it stays nil.
	if a.AvgPaceSecPerKm == nil {
		t.Fatal("AvgPaceSecPerKm = nil, want populated for ActivityRunning")
	}
	wantPace := float64(a.DurationSeconds) / (a.DistanceMeters / 1000)
	if math.Abs(*a.AvgPaceSecPerKm-wantPace) > 0.01 {
		t.Errorf("AvgPaceSecPerKm = %.3f, want %.3f", *a.AvgPaceSecPerKm, wantPace)
	}
	// StartTime is the first trackpoint's absolute time.
	if !a.StartTime.Equal(p.Trackpoints[0].Time) {
		t.Errorf("StartTime = %v, want %v", a.StartTime, p.Trackpoints[0].Time)
	}
	if a.ActivityType != ActivityRunning {
		t.Errorf("ActivityType = %q, want %q", a.ActivityType, ActivityRunning)
	}
}

// TestSummarize_NonRunningSkipsPace verifies that summarize leaves the
// pace fields nil when the caller passes a non-running activity type:
// pace is a running display concept and surfacing a "fastest 1km" on a
// walk would mislead at the UI layer.
func TestSummarize_NonRunningSkipsPace(t *testing.T) {
	p, err := parseTCX(readFixture(t, "biking.tcx"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := summarize(p, ActivityCycling)

	if a.AvgPaceSecPerKm != nil {
		t.Errorf("AvgPaceSecPerKm = %v, want nil for non-running", a.AvgPaceSecPerKm)
	}
	if a.BestPaceSecPerKm != nil {
		t.Errorf("BestPaceSecPerKm = %v, want nil for non-running", a.BestPaceSecPerKm)
	}
	if a.DistanceMeters <= 0 {
		t.Errorf("DistanceMeters = %v, want > 0", a.DistanceMeters)
	}
	if a.ActivityType != ActivityCycling {
		t.Errorf("ActivityType = %q, want %q", a.ActivityType, ActivityCycling)
	}
}

func TestSummarize_IntervalsBestPace(t *testing.T) {
	p, err := parseTCX(readFixture(t, "intervals.tcx"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := summarize(p, ActivityRunning)

	// The fixture's genuinely fastest km is 1000 m over 200 s => 200 s/km.
	// A per-sample heuristic would instead pick the 50 m/1 s GPS teleport
	// (~20 s/km). Asserting best is near 200 (and well above 20) proves the
	// 1 km distance-anchored window is in use, not the instantaneous min.
	if a.BestPaceSecPerKm == nil {
		t.Fatal("BestPaceSecPerKm is nil, want ~200")
	}
	best := *a.BestPaceSecPerKm
	if best < 190 || best > 230 {
		t.Errorf("BestPaceSecPerKm = %.2f, want ~200 (the fast km, not GPS noise)", best)
	}
	if best < 50 {
		t.Errorf("BestPaceSecPerKm = %.2f is implausibly fast: window ignored GPS jitter?", best)
	}

	// Downsampling preserves the endpoints exactly.
	tps := a.Trackpoints
	if len(tps) == 0 {
		t.Fatal("no downsampled trackpoints")
	}
	if tps[0].DistanceMeters != p.Trackpoints[0].DistanceMeters {
		t.Errorf("first kept distance = %.2f, want %.2f", tps[0].DistanceMeters, p.Trackpoints[0].DistanceMeters)
	}
	lastRaw := p.Trackpoints[len(p.Trackpoints)-1]
	if tps[len(tps)-1].DistanceMeters != lastRaw.DistanceMeters {
		t.Errorf("last kept distance = %.2f, want %.2f", tps[len(tps)-1].DistanceMeters, lastRaw.DistanceMeters)
	}

	// The peak-HR spike (195) must survive downsampling so the chart shape
	// is preserved. The fixture's HR max is 195.
	if a.MaxHeartRateBpm == nil || *a.MaxHeartRateBpm != 195 {
		t.Fatalf("MaxHeartRateBpm = %v, want 195", a.MaxHeartRateBpm)
	}
	foundPeak := false
	for _, tp := range tps {
		if tp.HeartRateBpm != nil && *tp.HeartRateBpm == 195 {
			foundPeak = true
			break
		}
	}
	if !foundPeak {
		t.Error("peak HR 195 not present among downsampled trackpoints")
	}
}

func TestSummarize_MarathonDownsampling(t *testing.T) {
	data := buildMarathonTCX(15000, 42000.0)
	p, err := parseTCX(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := summarize(p, ActivityRunning)

	if n := len(a.Trackpoints); n < 290 || n > 310 {
		t.Errorf("downsampled count = %d, want ~300", n)
	}

	first := a.Trackpoints[0]
	last := a.Trackpoints[len(a.Trackpoints)-1]
	if first.DistanceMeters != p.Trackpoints[0].DistanceMeters {
		t.Errorf("first kept distance = %.2f, want %.2f", first.DistanceMeters, p.Trackpoints[0].DistanceMeters)
	}
	if last.DistanceMeters != p.Trackpoints[len(p.Trackpoints)-1].DistanceMeters {
		t.Errorf("last kept distance = %.2f, want %.2f", last.DistanceMeters, p.Trackpoints[len(p.Trackpoints)-1].DistanceMeters)
	}
	if first.Sequence != 0 {
		t.Errorf("first Sequence = %d, want 0", first.Sequence)
	}
	if last.Sequence != len(a.Trackpoints)-1 {
		t.Errorf("last Sequence = %d, want %d", last.Sequence, len(a.Trackpoints)-1)
	}
}

func TestSummarize_PaceFilterStationaryStart(t *testing.T) {
	data := buildStationaryStartTCX(30, 0.1, 270, 3.0)
	p, err := parseTCX(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := summarize(p, ActivityRunning)

	if len(a.Trackpoints) < 100 {
		t.Fatalf("expected stride=1 to keep ~300 points, got %d", len(a.Trackpoints))
	}

	for i := 1; i < 30; i++ {
		if a.Trackpoints[i].PaceSecPerKm != nil {
			t.Errorf("trackpoint %d in stationary span has pace=%.2f, want nil",
				i, *a.Trackpoints[i].PaceSecPerKm)
		}
	}
	for _, i := range []int{80, 150, 250} {
		got := a.Trackpoints[i].PaceSecPerKm
		if got == nil {
			t.Errorf("trackpoint %d in jog span has nil pace, want ~333 s/km", i)
			continue
		}
		if *got < 300 || *got > 380 {
			t.Errorf("trackpoint %d pace = %.2f, want ~333 s/km (3 m/s)", i, *got)
		}
	}
}

// TestSummarize_BestEffortsEmbeddedFast5K pins the new BestEfforts field
// on a running activity with a fast 5K embedded inside a longer slower run.
// The 5K entry must reflect the embedded fast pace, and a too-long-for-the-
// activity distance (marathon) must be absent.
func TestSummarize_BestEffortsEmbeddedFast5K(t *testing.T) {
	// 8 km run at ~3.0 m/s with a 5 km block at ~4.2 m/s starting 1.5 km in.
	data := buildEmbeddedFast5KTCX()
	p, err := parseTCX(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := summarize(p, ActivityRunning)

	byKey := map[string]float64{}
	for _, e := range a.BestEfforts {
		byKey[e.DistanceKey] = e.DurationSeconds
	}

	// 1mi, 2mi, 5k present (total is 8 km); 10k/half/marathon absent.
	for _, k := range []string{"1mi", "2mi", "5k"} {
		if _, ok := byKey[k]; !ok {
			t.Errorf("expected a %q best effort", k)
		}
	}
	for _, k := range []string{"10k", "half_marathon", "marathon"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("did not expect a %q best effort on an 8 km run", k)
		}
	}

	// The 5K reflects the embedded ~4.2 m/s window (~1190 s), not the
	// ~3.0 m/s overall average (~1667 s).
	got5k, ok := byKey["5k"]
	if !ok {
		t.Fatal("missing 5k best effort")
	}
	want5kFast := 5000.0 / 4.2
	if math.Abs(got5k-want5kFast) > 30 {
		t.Errorf("5k best effort = %.1f, want ~%.1f (the embedded fast window)", got5k, want5kFast)
	}
}

// TestSummarize_WalkHasNoBestEfforts asserts a walk activity yields no best
// efforts even when its trace is long enough to cover standard distances.
func TestSummarize_WalkHasNoBestEfforts(t *testing.T) {
	data := buildEmbeddedFast5KTCX()
	p, err := parseTCX(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := summarize(p, ActivityWalking)
	if len(a.BestEfforts) != 0 {
		t.Errorf("walk produced %d best efforts, want 0", len(a.BestEfforts))
	}
}

// buildEmbeddedFast5KTCX builds an 8 km run at 1 Hz: ~3.0 m/s baseline with
// a 5 km block at ~4.2 m/s starting at 1500 m.
func buildEmbeddedFast5KTCX() []byte {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	const (
		total     = 8000.0
		fastStart = 1500.0
		fastEnd   = 6500.0
		slowMps   = 3.0
		fastMps   = 4.2
	)

	var dists []float64
	var elapsedSecs []int
	d := 0.0
	tSec := 0.0
	for d <= total {
		dists = append(dists, d)
		elapsedSecs = append(elapsedSecs, int(tSec))
		speed := slowMps
		if d >= fastStart && d < fastEnd {
			speed = fastMps
		}
		tSec += 1.0
		d += speed
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2">` + "\n")
	b.WriteString("  <Activities>\n")
	b.WriteString(`    <Activity Sport="Running">` + "\n")
	b.WriteString("      <Id>embedded-fast-5k-001</Id>\n")
	b.WriteString(`      <Lap StartTime="2026-01-02T08:00:00Z">` + "\n")
	fmt.Fprintf(&b, "        <TotalTimeSeconds>%d</TotalTimeSeconds>\n", elapsedSecs[len(elapsedSecs)-1])
	fmt.Fprintf(&b, "        <DistanceMeters>%.2f</DistanceMeters>\n", dists[len(dists)-1])
	b.WriteString("        <Track>\n")
	for i := range dists {
		ts := start.Add(time.Duration(elapsedSecs[i]) * time.Second).Format(time.RFC3339)
		fmt.Fprintf(&b, "          <Trackpoint><Time>%s</Time><DistanceMeters>%.2f</DistanceMeters></Trackpoint>\n", ts, dists[i])
	}
	b.WriteString("        </Track>\n")
	b.WriteString("      </Lap>\n")
	b.WriteString("    </Activity>\n")
	b.WriteString("  </Activities>\n")
	b.WriteString("</TrainingCenterDatabase>\n")
	return []byte(b.String())
}

func buildStationaryStartTCX(stationarySamples int, slowMps float64, runSamples int, fastMps float64) []byte {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	total := stationarySamples + runSamples

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2">` + "\n")
	b.WriteString("  <Activities>\n")
	b.WriteString(`    <Activity Sport="Running">` + "\n")
	b.WriteString("      <Id>stationary-start-001</Id>\n")
	b.WriteString(`      <Lap StartTime="2026-01-02T08:00:00Z">` + "\n")
	fmt.Fprintf(&b, "        <TotalTimeSeconds>%d</TotalTimeSeconds>\n", total-1)
	finalDist := float64(stationarySamples-1)*slowMps + float64(runSamples)*fastMps
	fmt.Fprintf(&b, "        <DistanceMeters>%.2f</DistanceMeters>\n", finalDist)
	b.WriteString("        <Track>\n")
	dist := 0.0
	for i := 0; i < total; i++ {
		if i > 0 {
			step := slowMps
			if i >= stationarySamples {
				step = fastMps
			}
			dist += step
		}
		ts := start.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		fmt.Fprintf(&b, "          <Trackpoint><Time>%s</Time><DistanceMeters>%.2f</DistanceMeters></Trackpoint>\n", ts, dist)
	}
	b.WriteString("        </Track>\n")
	b.WriteString("      </Lap>\n")
	b.WriteString("    </Activity>\n")
	b.WriteString("  </Activities>\n")
	b.WriteString("</TrainingCenterDatabase>\n")
	return []byte(b.String())
}

func buildMarathonTCX(n int, totalDist float64) []byte {
	start := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	step := totalDist / float64(n-1)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2">` + "\n")
	b.WriteString("  <Activities>\n")
	b.WriteString(`    <Activity Sport="Running">` + "\n")
	b.WriteString("      <Id>marathon-001</Id>\n")
	b.WriteString(`      <Lap StartTime="2026-01-02T08:00:00Z">` + "\n")
	fmt.Fprintf(&b, "        <TotalTimeSeconds>%d</TotalTimeSeconds>\n", n-1)
	fmt.Fprintf(&b, "        <DistanceMeters>%.2f</DistanceMeters>\n", totalDist)
	b.WriteString("        <Track>\n")
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		fmt.Fprintf(&b, "          <Trackpoint><Time>%s</Time><DistanceMeters>%.2f</DistanceMeters></Trackpoint>\n", ts, step*float64(i))
	}
	b.WriteString("        </Track>\n")
	b.WriteString("      </Lap>\n")
	b.WriteString("    </Activity>\n")
	b.WriteString("  </Activities>\n")
	b.WriteString("</TrainingCenterDatabase>\n")
	return []byte(b.String())
}
