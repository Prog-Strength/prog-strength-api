package activity

import (
	"strings"
	"testing"
)

// alignedActivity builds an Activity + Derivation pair that satisfies every
// invariant: steady 5.3 km, summary fields recomputed from the same stream.
func alignedActivity(t *testing.T) (Activity, Derivation) {
	t.Helper()
	tps := steadyTrack(53, 100, 30) // 5300 m in 1590 s
	avg := 1590.0 / 5.3
	best := bestRollingPace(tps, 1000)
	a := Activity{
		ActivityType:    ActivityRunning,
		DistanceMeters:  5300,
		DurationSeconds: 1590,
		AvgPaceSecPerKm: &avg,
		Trackpoints:     tps,
	}
	_ = best
	return a, deriveRunning(tps, UnitKm)
}

func TestCheckDetailInvariants_AlignedIsClean(t *testing.T) {
	a, d := alignedActivity(t)
	if v := checkDetailInvariants(a, d, UnitKm, nil); len(v) != 0 {
		t.Fatalf("aligned activity reported violations: %v", v)
	}
}

// Each mutation below breaks exactly the invariant named in the violation.
func TestCheckDetailInvariants_CatchesDrift(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(a *Activity, d *Derivation)
		wantSub string
	}{
		{"stored distance drifts from stream", func(a *Activity, d *Derivation) {
			a.DistanceMeters += 50
		}, "I1"},
		{"split time drops a segment", func(a *Activity, d *Derivation) {
			d.Splits[0].DurationSeconds -= 30
		}, "I2"},
		{"split pace not time-over-dist", func(a *Activity, d *Derivation) {
			p := *d.Splits[0].PaceSecPerUnit + 20
			d.Splits[0].PaceSecPerUnit = &p
		}, "I3"},
		{"stored avg pace stale", func(a *Activity, d *Derivation) {
			p := *a.AvgPaceSecPerKm + 10
			a.AvgPaceSecPerKm = &p
		}, "I5"},
		{"best slower than fastest split", func(a *Activity, d *Derivation) {
			p := 1e9
			d.BestPaceSecPerUnit = &p
		}, "I6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, d := alignedActivity(t)
			tc.mutate(&a, &d)
			v := checkDetailInvariants(a, d, UnitKm, nil)
			if len(v) == 0 {
				t.Fatal("expected a violation")
			}
			found := false
			for _, s := range v {
				if strings.Contains(s, tc.wantSub) {
					found = true
				}
			}
			if !found {
				t.Errorf("violations %v missing %q", v, tc.wantSub)
			}
		})
	}
}

// TestCheckDetailInvariants_HRZones: zone seconds must fit inside the
// duration and percentages must sum to ~1.
func TestCheckDetailInvariants_HRZones(t *testing.T) {
	a, d := alignedActivity(t)
	good := &heartRateZonesDTO{TotalHRSeconds: 1500, Zones: []heartRateZoneDTO{
		{TimeSeconds: 900, TimePct: 0.6}, {TimeSeconds: 600, TimePct: 0.4},
	}}
	if v := checkDetailInvariants(a, d, UnitKm, good); len(v) != 0 {
		t.Fatalf("good zones reported violations: %v", v)
	}
	overflow := &heartRateZonesDTO{TotalHRSeconds: 2000, Zones: []heartRateZoneDTO{
		{TimeSeconds: 2000, TimePct: 1.0},
	}}
	if v := checkDetailInvariants(a, d, UnitKm, overflow); len(v) == 0 {
		t.Fatal("zone seconds exceeding duration should violate I7")
	}
	badPct := &heartRateZonesDTO{TotalHRSeconds: 1500, Zones: []heartRateZoneDTO{
		{TimeSeconds: 900, TimePct: 0.6}, {TimeSeconds: 600, TimePct: 0.3},
	}}
	if v := checkDetailInvariants(a, d, UnitKm, badPct); len(v) == 0 {
		t.Fatal("zone pct summing to 0.9 should violate I7")
	}
}
