package hrzones

import (
	"math"
	"testing"
)

// defaultConfig mirrors the [hr_zones] defaults used across the suite.
func defaultConfig() Config {
	return Config{
		PopulationDefaultMaxHR: 190,
		CalibratedRunThreshold: 5,
		RecencyWindowDays:      90,
		MinReferenceBpm:        100,
		MaxReferenceBpm:        230,
		ZoneUpperBounds:        []float64{0.60, 0.70, 0.80, 0.90},
		ZoneNames:              []string{"Recovery", "Aerobic", "Tempo", "Threshold", "VO2max"},
	}
}

func ptrInt(v int) *int { return &v }

func TestZoneBpmBounds(t *testing.T) {
	e := New(defaultConfig())
	ref := Reference{MaxHRBpm: 191, Source: "p99_recent_runs", Confidence: ConfidenceCalibrated}

	// Single all-HR interval guarantees a usable result we can read zone bounds off.
	tps := []Trackpoint{
		{ElapsedSeconds: 0, HeartRateBpm: ptrInt(150)},
		{ElapsedSeconds: 10, HeartRateBpm: ptrInt(150)},
	}
	res, ok := e.Compute(ref, tps)
	if !ok {
		t.Fatalf("expected ok result")
	}
	if len(res.Zones) != 5 {
		t.Fatalf("expected 5 zones, got %d", len(res.Zones))
	}

	want := []struct {
		number         int
		name           string
		minBpm, maxBpm int
	}{
		{1, "Recovery", 0, 114},
		{2, "Aerobic", 115, 133},
		{3, "Tempo", 134, 152},
		{4, "Threshold", 153, 171},
		{5, "VO2max", 172, 191},
	}
	for i, w := range want {
		z := res.Zones[i]
		if z.Number != w.number || z.Name != w.name || z.MinBpm != w.minBpm || z.MaxBpm != w.maxBpm {
			t.Errorf("zone %d = {num:%d name:%q min:%d max:%d}, want {num:%d name:%q min:%d max:%d}",
				i+1, z.Number, z.Name, z.MinBpm, z.MaxBpm, w.number, w.name, w.minBpm, w.maxBpm)
		}
	}
}

// TestZoneBoundsMatchClassification pins the invariant that the displayed
// [MinBpm, MaxBpm] of each zone agrees exactly with where Compute/classify
// attributes time. It uses maxHR=188, a "rounds-down" reference: e.g.
// 0.80*188 = 150.4, so a value of 150 bpm classifies into Z3 (150 < 150.4).
// With math.Round the display would have shown Z3 ending at 149 and Z4 starting
// at 150 — contradicting the classification. With math.Ceil they agree.
func TestZoneBoundsMatchClassification(t *testing.T) {
	e := New(defaultConfig())
	const maxHR = 188
	ref := Reference{MaxHRBpm: maxHR, Confidence: ConfidenceCalibrated}

	res, ok := e.Compute(ref, []Trackpoint{
		{ElapsedSeconds: 0, HeartRateBpm: ptrInt(150)},
		{ElapsedSeconds: 1, HeartRateBpm: ptrInt(150)},
	})
	if !ok {
		t.Fatalf("expected ok result")
	}
	zones := res.Zones

	// classifyBpm returns the 1-indexed zone number Compute attributes a flat
	// interval at the given bpm to.
	classifyBpm := func(bpm int) int {
		r, ok := e.Compute(ref, []Trackpoint{
			{ElapsedSeconds: 0, HeartRateBpm: ptrInt(bpm)},
			{ElapsedSeconds: 1, HeartRateBpm: ptrInt(bpm)},
		})
		if !ok {
			t.Fatalf("expected ok for bpm=%d", bpm)
		}
		for _, z := range r.Zones {
			if z.TimeSeconds > 0 {
				return z.Number
			}
		}
		t.Fatalf("no zone accumulated time for bpm=%d", bpm)
		return 0
	}

	// zoneContaining returns the 1-indexed zone whose displayed [MinBpm, MaxBpm]
	// range contains bpm, or 0 if none.
	zoneContaining := func(bpm int) int {
		for _, z := range zones {
			if bpm >= z.MinBpm && bpm <= z.MaxBpm {
				return z.Number
			}
		}
		return 0
	}

	// For every integer bpm across the full range, the displayed zone whose
	// range contains it must equal the zone classify actually counts it in. The
	// displayed ranges must also be contiguous and exhaustive (every bpm lands
	// in exactly one zone), which zoneContaining returning a non-zero match for
	// every bpm enforces.
	for bpm := 0; bpm <= maxHR; bpm++ {
		display := zoneContaining(bpm)
		if display == 0 {
			t.Errorf("bpm=%d falls in no displayed zone range", bpm)
			continue
		}
		if got := classifyBpm(bpm); got != display {
			t.Errorf("bpm=%d: classify -> Z%d, but displayed range -> Z%d", bpm, got, display)
		}
	}

	// Spell out the rounds-down boundary explicitly so a regression is obvious:
	// 0.80*188 = 150.4 -> Z3 = [..,150], Z4 = [151,..]; classify(150) must be Z3.
	if got := classifyBpm(150); got != 3 {
		t.Errorf("classify(150) at maxHR=188 = Z%d, want Z3", got)
	}
	if zones[2].MaxBpm != 150 {
		t.Errorf("Z3 MaxBpm = %d, want 150", zones[2].MaxBpm)
	}
	if zones[3].MinBpm != 151 {
		t.Errorf("Z4 MinBpm = %d, want 151", zones[3].MinBpm)
	}
}

func TestClassifyBoundaries(t *testing.T) {
	e := New(defaultConfig())
	const maxHR = 191
	ref := Reference{MaxHRBpm: maxHR, Confidence: ConfidenceCalibrated}

	// classifyZone via Compute over a single flat interval at the given mean.
	classify := func(mean int) int {
		tps := []Trackpoint{
			{ElapsedSeconds: 0, HeartRateBpm: ptrInt(mean)},
			{ElapsedSeconds: 1, HeartRateBpm: ptrInt(mean)},
		}
		res, ok := e.Compute(ref, tps)
		if !ok {
			t.Fatalf("expected ok for mean=%d", mean)
		}
		for _, z := range res.Zones {
			if z.TimeSeconds > 0 {
				return z.Number
			}
		}
		t.Fatalf("no zone accumulated time for mean=%d", mean)
		return 0
	}

	tests := []struct {
		name string
		mean int
		want int
	}{
		{"very low -> Z1", 50, 1},
		{"just below 0.60*max (114) -> Z1", 114, 1}, // 114 < 114.6
		{"just above 0.60*max (115) -> Z2", 115, 2}, // 115 >= 114.6
		{"just below 0.70*max (133) -> Z2", 133, 2}, // 133 < 133.7
		{"at/above 0.70*max (134) -> Z3", 134, 3},   // 134 >= 133.7
		{"just below 0.80*max (152) -> Z3", 152, 3}, // 152 < 152.8
		{"at/above 0.80*max (153) -> Z4", 153, 4},   // 153 >= 152.8
		{"just below 0.90*max (171) -> Z4", 171, 4}, // 171 < 171.9
		{"at/above 0.90*max (172) -> Z5", 172, 5},   // 172 >= 171.9
		{"max -> Z5", 191, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.mean); got != tt.want {
				t.Errorf("classify(mean=%d) = Z%d, want Z%d", tt.mean, got, tt.want)
			}
		})
	}
}

func TestComputeTimeInZone(t *testing.T) {
	e := New(defaultConfig())
	ref := Reference{MaxHRBpm: 191, Confidence: ConfidenceCalibrated}

	// Mixed 9s/10s dt, plus a null-HR pair that must be skipped.
	tps := []Trackpoint{
		{ElapsedSeconds: 0, HeartRateBpm: ptrInt(100)},  // pair0->1: mean 100 -> Z1, dt 10
		{ElapsedSeconds: 10, HeartRateBpm: ptrInt(100)}, // pair1->2: mean 120 -> Z2, dt 9
		{ElapsedSeconds: 19, HeartRateBpm: ptrInt(140)}, // pair2->3: null HR endpoint -> skip, dt 10
		{ElapsedSeconds: 29, HeartRateBpm: nil},         // pair3->4: null HR endpoint -> skip, dt 9
		{ElapsedSeconds: 38, HeartRateBpm: ptrInt(160)}, // pair4->5: mean 160 -> Z4, dt 10
		{ElapsedSeconds: 48, HeartRateBpm: ptrInt(160)},
	}
	res, ok := e.Compute(ref, tps)
	if !ok {
		t.Fatalf("expected ok")
	}

	// Z1 mean 100, Z2 mean 120, Z4 mean 160. dt 10 + 9 + 10 = 29 total.
	if res.TotalHRSeconds != 29 {
		t.Errorf("TotalHRSeconds = %d, want 29", res.TotalHRSeconds)
	}
	wantTime := map[int]int{1: 10, 2: 9, 3: 0, 4: 10, 5: 0}
	for _, z := range res.Zones {
		if z.TimeSeconds != wantTime[z.Number] {
			t.Errorf("zone %d TimeSeconds = %d, want %d", z.Number, z.TimeSeconds, wantTime[z.Number])
		}
	}

	var sumPct float64
	for _, z := range res.Zones {
		sumPct += z.TimePct
	}
	if math.Abs(sumPct-1.0) > 1e-9 {
		t.Errorf("sum(TimePct) = %v, want ~1.0", sumPct)
	}

	if res.Model != "percent_max_hr" {
		t.Errorf("Model = %q, want percent_max_hr", res.Model)
	}
	if res.Calibrating {
		t.Errorf("Calibrating = true, want false for calibrated reference")
	}
}

func TestComputeCalibratingFlag(t *testing.T) {
	e := New(defaultConfig())
	tps := []Trackpoint{
		{ElapsedSeconds: 0, HeartRateBpm: ptrInt(150)},
		{ElapsedSeconds: 10, HeartRateBpm: ptrInt(150)},
	}
	for _, c := range []Confidence{ConfidenceEstimated, ConfidenceCalibrating} {
		res, ok := e.Compute(Reference{MaxHRBpm: 191, Confidence: c}, tps)
		if !ok {
			t.Fatalf("expected ok for confidence %q", c)
		}
		if !res.Calibrating {
			t.Errorf("Calibrating = false for confidence %q, want true", c)
		}
	}
}

func TestComputeNoHR(t *testing.T) {
	e := New(defaultConfig())
	ref := Reference{MaxHRBpm: 191, Confidence: ConfidenceCalibrated}

	cases := map[string][]Trackpoint{
		"all nil": {
			{ElapsedSeconds: 0, HeartRateBpm: nil},
			{ElapsedSeconds: 10, HeartRateBpm: nil},
		},
		"single HR point": {
			{ElapsedSeconds: 0, HeartRateBpm: ptrInt(150)},
			{ElapsedSeconds: 10, HeartRateBpm: nil},
		},
		"empty": {},
	}
	for name, tps := range cases {
		t.Run(name, func(t *testing.T) {
			res, ok := e.Compute(ref, tps)
			if ok {
				t.Errorf("expected ok=false, got true with result %+v", res)
			}
		})
	}
}

func TestEstimateReference(t *testing.T) {
	e := New(defaultConfig())

	tests := []struct {
		name           string
		stats          Stats
		wantBpm        int
		wantSource     string
		wantConfidence Confidence
	}{
		{
			name:           "cold start no data",
			stats:          Stats{},
			wantBpm:        190,
			wantSource:     "population_default",
			wantConfidence: ConfidenceEstimated,
		},
		{
			name:           "cold start current run above default",
			stats:          Stats{CurrentRunP99: ptrInt(200)},
			wantBpm:        200,
			wantSource:     "current_run",
			wantConfidence: ConfidenceEstimated,
		},
		{
			name:           "cold start current run below default",
			stats:          Stats{CurrentRunP99: ptrInt(150)},
			wantBpm:        190,
			wantSource:     "population_default",
			wantConfidence: ConfidenceEstimated,
		},
		{
			name:           "calibrating uses max of recent p99 and current",
			stats:          Stats{HistoryRunCount: 2, RecentHRSamplesP99: ptrInt(180), CurrentRunP99: ptrInt(195)},
			wantBpm:        195,
			wantSource:     "p99_recent_runs",
			wantConfidence: ConfidenceCalibrating,
		},
		{
			name:           "calibrating with nil current run",
			stats:          Stats{HistoryRunCount: 2, RecentHRSamplesP99: ptrInt(180)},
			wantBpm:        180,
			wantSource:     "p99_recent_runs",
			wantConfidence: ConfidenceCalibrating,
		},
		{
			name:           "calibrated uses recent p99",
			stats:          Stats{HistoryRunCount: 5, RecentHRSamplesP99: ptrInt(191)},
			wantBpm:        191,
			wantSource:     "p99_recent_runs",
			wantConfidence: ConfidenceCalibrated,
		},
		{
			name:           "clamp ceiling",
			stats:          Stats{CurrentRunP99: ptrInt(250)},
			wantBpm:        230,
			wantSource:     "current_run",
			wantConfidence: ConfidenceEstimated,
		},
		{
			name:           "clamp floor",
			stats:          Stats{HistoryRunCount: 5, RecentHRSamplesP99: ptrInt(80)},
			wantBpm:        100,
			wantSource:     "p99_recent_runs",
			wantConfidence: ConfidenceCalibrated,
		},
		{
			name: "spike rejection via p99 not max",
			// RecentHRSamplesP99 is fed as P99 of a set containing a lone 220 spike.
			stats:          Stats{HistoryRunCount: 5, RecentHRSamplesP99: spikeSetP99(t)},
			wantBpm:        191,
			wantSource:     "p99_recent_runs",
			wantConfidence: ConfidenceCalibrated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := e.EstimateReference(tt.stats)
			if ref.MaxHRBpm != tt.wantBpm {
				t.Errorf("MaxHRBpm = %d, want %d", ref.MaxHRBpm, tt.wantBpm)
			}
			if ref.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", ref.Source, tt.wantSource)
			}
			if ref.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %q, want %q", ref.Confidence, tt.wantConfidence)
			}
		})
	}
}

// spikeSetP99 builds a recent-sample p99 from a set whose only high value is a
// lone 220 spike; the p99 must resolve to 191, not the 220 max.
func spikeSetP99(t *testing.T) *int {
	t.Helper()
	samples := make([]int, 0, 100)
	for i := 0; i < 99; i++ {
		samples = append(samples, 191)
	}
	samples = append(samples, 220)
	p := P99(samples)
	if p == nil || *p != 191 {
		t.Fatalf("spike set p99 = %v, want 191", p)
	}
	return p
}

func TestP99(t *testing.T) {
	t.Run("empty -> nil", func(t *testing.T) {
		if got := P99(nil); got != nil {
			t.Errorf("P99(nil) = %v, want nil", *got)
		}
	})
	t.Run("single sample", func(t *testing.T) {
		got := P99([]int{142})
		if got == nil || *got != 142 {
			t.Errorf("P99([142]) = %v, want 142", got)
		}
	})
	t.Run("nearest-rank", func(t *testing.T) {
		// 1..100: rank = ceil(0.99*100) = 99 -> sorted[98] = 99.
		samples := make([]int, 0, 100)
		for v := 1; v <= 100; v++ {
			samples = append(samples, v)
		}
		got := P99(samples)
		if got == nil || *got != 99 {
			t.Errorf("P99(1..100) = %v, want 99", got)
		}
	})
	t.Run("spike does not become p99", func(t *testing.T) {
		// 99 samples of 150 plus a lone spike of 220. n=100, rank 99 -> 150.
		samples := make([]int, 0, 100)
		for i := 0; i < 99; i++ {
			samples = append(samples, 150)
		}
		samples = append(samples, 220)
		got := P99(samples)
		if got == nil || *got != 150 {
			t.Errorf("P99 with lone 220 spike = %v, want 150", got)
		}
	})
}
