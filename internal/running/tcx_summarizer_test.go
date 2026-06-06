package running

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
	s := summarize(p)

	// Distance: 5000 m exactly by construction (±10 m tolerance).
	if math.Abs(s.DistanceMeters-5000) > 10 {
		t.Errorf("DistanceMeters = %.2f, want ~5000", s.DistanceMeters)
	}
	// 600 points at 1 Hz => 599 s span (±1 s).
	if s.DurationSeconds < 598 || s.DurationSeconds > 600 {
		t.Errorf("DurationSeconds = %d, want ~599", s.DurationSeconds)
	}
	// HR alternates 140/160 => mean exactly 150.
	if s.AvgHeartRateBpm == nil || *s.AvgHeartRateBpm != 150 {
		t.Errorf("AvgHeartRateBpm = %v, want 150", s.AvgHeartRateBpm)
	}
	if s.MaxHeartRateBpm == nil || *s.MaxHeartRateBpm != 160 {
		t.Errorf("MaxHeartRateBpm = %v, want 160", s.MaxHeartRateBpm)
	}
	// Calories summed from the single lap.
	if s.TotalCalories == nil || *s.TotalCalories != 350 {
		t.Errorf("TotalCalories = %v, want 350", s.TotalCalories)
	}
	// Altitude climbs 100->150 (gain 50) then descends; gain ~50 m (±1).
	if s.ElevationGainMeters == nil || math.Abs(*s.ElevationGainMeters-50) > 1 {
		t.Errorf("ElevationGainMeters = %v, want ~50", s.ElevationGainMeters)
	}
	// Avg pace = 599 s / 5 km ~= 119.8 s/km.
	wantPace := float64(s.DurationSeconds) / (s.DistanceMeters / 1000)
	if math.Abs(s.AvgPaceSecPerKm-wantPace) > 0.01 {
		t.Errorf("AvgPaceSecPerKm = %.3f, want %.3f", s.AvgPaceSecPerKm, wantPace)
	}
	// StartTime is the first trackpoint's absolute time.
	if !s.StartTime.Equal(p.Trackpoints[0].Time) {
		t.Errorf("StartTime = %v, want %v", s.StartTime, p.Trackpoints[0].Time)
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
	s := summarize(p)

	// The fixture's genuinely fastest km is 1000 m over 200 s => 200 s/km.
	// A per-sample heuristic would instead pick the 50 m/1 s GPS teleport
	// (~20 s/km). Asserting best is near 200 (and well above 20) proves the
	// 1 km distance-anchored window is in use, not the instantaneous min.
	if s.BestPaceSecPerKm == nil {
		t.Fatal("BestPaceSecPerKm is nil, want ~200")
	}
	best := *s.BestPaceSecPerKm
	if best < 190 || best > 230 {
		t.Errorf("BestPaceSecPerKm = %.2f, want ~200 (the fast km, not GPS noise)", best)
	}
	if best < 50 {
		t.Errorf("BestPaceSecPerKm = %.2f is implausibly fast: window ignored GPS jitter?", best)
	}

	// Downsampling preserves the endpoints exactly.
	tps := s.Trackpoints
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
	if s.MaxHeartRateBpm == nil || *s.MaxHeartRateBpm != 195 {
		t.Fatalf("MaxHeartRateBpm = %v, want 195", s.MaxHeartRateBpm)
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
	// marathon.tcx is generated here (in TempDir) rather than committed:
	// ~15k points is a ~2 MB file we don't want in git. The summarizer only
	// needs the parsed bytes, so building them on the fly is equivalent.
	data := buildMarathonTCX(15000, 42000.0)
	p, err := parseTCX(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	s := summarize(p)

	// stride = 15000/300 = 50 => 300 strided points, plus the forced final
	// point (index 14999, not on a 50-stride boundary) => 301.
	if n := len(s.Trackpoints); n < 290 || n > 310 {
		t.Errorf("downsampled count = %d, want ~300", n)
	}

	first := s.Trackpoints[0]
	last := s.Trackpoints[len(s.Trackpoints)-1]
	if first.DistanceMeters != p.Trackpoints[0].DistanceMeters {
		t.Errorf("first kept distance = %.2f, want %.2f", first.DistanceMeters, p.Trackpoints[0].DistanceMeters)
	}
	if last.DistanceMeters != p.Trackpoints[len(p.Trackpoints)-1].DistanceMeters {
		t.Errorf("last kept distance = %.2f, want %.2f", last.DistanceMeters, p.Trackpoints[len(p.Trackpoints)-1].DistanceMeters)
	}
	// Sequence is the kept-point index, contiguous from 0.
	if first.Sequence != 0 {
		t.Errorf("first Sequence = %d, want 0", first.Sequence)
	}
	if last.Sequence != len(s.Trackpoints)-1 {
		t.Errorf("last Sequence = %d, want %d", last.Sequence, len(s.Trackpoints)-1)
	}
}

func TestSummarize_PaceFilterStationaryStart(t *testing.T) {
	// A real-world TCX often starts with the watch recording before the user
	// actually moves: GPS-fix wandering produces tiny sub-meter distance
	// deltas at 1 Hz, which translate to absurd minute-per-km values that
	// dominate the chart's Y range. The filter nulls out kept-point pace
	// when the segment's instantaneous speed is below paceFilterMinSpeedMps
	// (0.5 m/s ≈ 53:36 min/mile), well below any deliberate walking pace.
	//
	// The fixture is 30 s of GPS noise at 0.1 m/s followed by 270 s of
	// jogging at 3 m/s, all at 1 Hz so stride=1 and every raw point is
	// kept. Kept points in the noise region must come back with nil pace;
	// kept points in the jog region must carry a sensible pace.
	data := buildStationaryStartTCX(30, 0.1, 270, 3.0)
	p, err := parseTCX(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	s := summarize(p)

	if len(s.Trackpoints) < 100 {
		t.Fatalf("expected stride=1 to keep ~300 points, got %d", len(s.Trackpoints))
	}

	// Index 0 has no prior segment — pace is nil by existing convention,
	// not by the filter. Indices 1..29 sit inside the GPS-noise span and
	// must come back filtered. Indices well past 30 sit in the jog region
	// and must carry a finite pace.
	for i := 1; i < 30; i++ {
		if s.Trackpoints[i].PaceSecPerKm != nil {
			t.Errorf("trackpoint %d in stationary span has pace=%.2f, want nil",
				i, *s.Trackpoints[i].PaceSecPerKm)
		}
	}
	// A handful of well-into-the-jog samples — they should ALL carry a
	// reasonable jogging pace (~333 s/km for 3 m/s).
	for _, i := range []int{80, 150, 250} {
		got := s.Trackpoints[i].PaceSecPerKm
		if got == nil {
			t.Errorf("trackpoint %d in jog span has nil pace, want ~333 s/km", i)
			continue
		}
		if *got < 300 || *got > 380 {
			t.Errorf("trackpoint %d pace = %.2f, want ~333 s/km (3 m/s)", i, *got)
		}
	}
}

// buildStationaryStartTCX emits a deterministic Running TCX whose first
// stationarySamples points crawl at slowMps m/s (GPS noise on a still
// watch) and whose next runSamples points run at fastMps m/s. All at 1 Hz.
// Used to verify the pace outlier filter without committing a fixture.
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

// buildMarathonTCX emits a deterministic Running TCX with n points evenly
// spaced over totalDist meters at 1 Hz. Kept minimal: no HR/altitude, just
// enough to exercise stride math.
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
