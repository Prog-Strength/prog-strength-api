package activity

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// bruteRightEdge is the reference: anchor each left sample, interpolate the
// right edge at left.dist+T. The production sweep must match it exactly.
func bruteRightEdge(tps []parsedTrackpoint, T float64) (float64, bool) {
	n := len(tps)
	if n < 2 || tps[n-1].DistanceMeters-tps[0].DistanceMeters < T {
		return 0, false
	}
	best := math.Inf(1)
	for l := 0; l < n; l++ {
		te := tps[l].DistanceMeters + T
		if te > tps[n-1].DistanceMeters {
			break
		}
		for p := l; p+1 < n; p++ {
			if tps[p].DistanceMeters <= te && te <= tps[p+1].DistanceMeters {
				segD := tps[p+1].DistanceMeters - tps[p].DistanceMeters
				if segD <= 0 {
					continue
				}
				ratio := (te - tps[p].DistanceMeters) / segD
				endT := tps[p].Time.Add(time.Duration(ratio * float64(tps[p+1].Time.Sub(tps[p].Time))))
				w := endT.Sub(tps[l].Time).Seconds()
				if w < best {
					best = w
				}
				break
			}
		}
	}
	if math.IsInf(best, 1) {
		return 0, false
	}
	return best, true
}

func TestBestEfforts_FuzzAgainstBrute(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 50000; iter++ {
		n := 2 + rng.Intn(14)
		tps := make([]parsedTrackpoint, n)
		d, ts := 0.0, 0.0
		for i := 0; i < n; i++ {
			tps[i] = parsedTrackpoint{Time: base.Add(time.Duration(ts * float64(time.Second))), DistanceMeters: d}
			step := rng.Float64() * 900
			if rng.Intn(12) == 0 {
				step = 0
			}
			d += step
			ts += rng.Float64()*120 + 0.05
		}
		T := rng.Float64() * 2200
		efforts := bestEfforts(tps, []StandardDistance{{Key: "x", Meters: T}})
		var got float64
		gotOK := false
		for _, e := range efforts {
			if e.DistanceKey == "x" {
				got, gotOK = e.DurationSeconds, true
			}
		}
		want, wantOK := bruteRightEdge(tps, T)
		if gotOK != wantOK || (gotOK && math.Abs(got-want) > 1e-6) {
			t.Fatalf("iter %d T=%.3f: sweep=%.6f(%v) brute=%.6f(%v)", iter, T, got, gotOK, want, wantOK)
		}
	}
}
