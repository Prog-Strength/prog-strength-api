package activity

import (
	"math/rand"
	"testing"
)

// TestInvariants_RandomTracksAlwaysAlign is the SOW's property test: for any
// plausible monotonic track, the summary recomputed the way ingest computes
// it plus the read-time derivation must pass the invariant gate. Seeded PRNG
// keeps failures reproducible.
func TestInvariants_RandomTracksAlwaysAlign(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 200; trial++ {
		n := 2 + rng.Intn(400)
		tps := make([]Trackpoint, 0, n)
		dist, elapsed := 0.0, 0
		for i := 0; i < n; i++ {
			var pace *float64
			if i > 0 {
				dStep := rng.Float64() * 60 // 0–60 m per step (incl. stationary)
				tStep := 1 + rng.Intn(30)
				dist += dStep
				elapsed += tStep
				if dStep > 0.5 {
					p := float64(tStep) / (dStep / 1000)
					// Leave some samples nil (stationary filter) at random.
					if rng.Float64() > 0.1 {
						pace = &p
					}
				}
			}
			tps = append(tps, Trackpoint{Sequence: i, ElapsedSeconds: elapsed, DistanceMeters: dist, PaceSecPerKm: pace})
		}
		if dist <= 0 {
			continue
		}
		avg := float64(elapsed) / (dist / 1000)
		a := Activity{
			ActivityType:    ActivityRunning,
			DistanceMeters:  dist,
			DurationSeconds: elapsed,
			AvgPaceSecPerKm: &avg,
			Trackpoints:     tps,
		}
		for _, unit := range []DistanceUnit{UnitMiles, UnitKm} {
			d := deriveRunning(tps, unit)
			if v := checkDetailInvariants(a, d, unit, nil); len(v) != 0 {
				t.Fatalf("trial %d unit %s: violations %v", trial, unit, v)
			}
		}
	}
}
