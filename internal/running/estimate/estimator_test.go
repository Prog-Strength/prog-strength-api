package estimate

import (
	"math"
	"testing"
	"time"
)

// refNow is the fixed clock all tests inject so recency weighting (and thus
// every result) is deterministic.
var refNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// distMeters maps the standard distance keys the tests use to meters, so the
// effort builder reads like real running data.
var distMeters = map[string]float64{
	"1mi":           1609.344,
	"2mi":           3218.688,
	"5k":            5000,
	"10k":           10000,
	"half_marathon": 21097.5,
	"marathon":      42195,
}

// effort builds an Attempt at a standard distance, daysAgo before refNow,
// from a race-like activity (activity distance == effort distance).
func effort(key string, durationSeconds float64, daysAgo int) Attempt {
	d := distMeters[key]
	return Attempt{
		DistanceKey:            key,
		DistanceMeters:         d,
		DurationSeconds:        durationSeconds,
		AchievedAt:             refNow.Add(-time.Duration(daysAgo) * 24 * time.Hour),
		ActivityDistanceMeters: d,
	}
}

// riegelProject returns the classic Riegel projection of a (dFrom, tFrom)
// effort to dTarget with exponent b — the analytic baseline tests compare to.
func riegelProject(tFrom, dFrom, dTarget, b float64) float64 {
	return tFrom * math.Pow(dTarget/dFrom, b)
}

func intPtr(v int) *int         { return &v }
func strPtr(v string) *string   { return &v }
func f64Ptr(v float64) *float64 { return &v }

func TestEstimate_ZeroEffortsWithDemographics(t *testing.T) {
	est := NewEstimator()
	in := EstimateInput{
		TargetDistanceKey:    "5k",
		TargetDistanceMeters: distMeters["5k"],
		Demographics:         Demographics{Age: intPtr(30), Sex: strPtr("male")},
		Now:                  refNow,
	}
	r := est.Estimate(in)

	if r.Basis != "demographic_prior" {
		t.Fatalf("Basis = %q, want demographic_prior", r.Basis)
	}
	if r.Seconds <= 0 {
		t.Fatalf("Seconds = %v, want > 0", r.Seconds)
	}
	if r.Confidence != "low" {
		t.Errorf("Confidence = %q, want low (band should be wide with no data)", r.Confidence)
	}
	if r.NPoints != 0 || r.NDistances != 0 {
		t.Errorf("NPoints/NDistances = %d/%d, want 0/0", r.NPoints, r.NDistances)
	}
	if r.Version != EstimatorVersion {
		t.Errorf("Version = %q, want %q", r.Version, EstimatorVersion)
	}
}

func TestEstimate_ZeroEffortsNoDemographics(t *testing.T) {
	est := NewEstimator()
	r := est.Estimate(EstimateInput{
		TargetDistanceKey:    "5k",
		TargetDistanceMeters: distMeters["5k"],
		Now:                  refNow,
	})
	if r.Basis != "insufficient_data" {
		t.Fatalf("Basis = %q, want insufficient_data", r.Basis)
	}
	if r.Seconds != 0 || r.LowerSeconds != 0 || r.UpperSeconds != 0 {
		t.Errorf("expected all-zero seconds, got %v/%v/%v", r.Seconds, r.LowerSeconds, r.UpperSeconds)
	}
	if r.Version != EstimatorVersion {
		t.Errorf("Version = %q, want %q", r.Version, EstimatorVersion)
	}
}

func TestEstimate_SingleEffortRiegelProjection(t *testing.T) {
	est := NewEstimator()
	// A 5K in 20:00, projecting up to the marathon.
	fast := 1200.0
	in := EstimateInput{
		TargetDistanceKey:    "marathon",
		TargetDistanceMeters: distMeters["marathon"],
		Attempts:             []Attempt{effort("5k", fast, 10)},
		Now:                  refNow,
	}
	r := est.Estimate(in)

	if r.Basis != "single_effort" {
		t.Fatalf("Basis = %q, want single_effort", r.Basis)
	}
	want := riegelProject(fast, distMeters["5k"], distMeters["marathon"], priorBeta1Mean)
	relErr := math.Abs(r.Seconds-want) / want
	if relErr > 0.03 {
		t.Errorf("Seconds = %.1f, want ~%.1f (rel err %.3f > 0.03)", r.Seconds, want, relErr)
	}
}

func TestEstimate_MultiDistanceFittedCurve(t *testing.T) {
	est := NewEstimator()
	// A realistic, internally-consistent set: 1mi 5:30, 5k 19:00, 10k 39:30.
	// The implied exponent across these is a touch above 1.06.
	attempts := []Attempt{
		effort("1mi", 330, 20),
		effort("5k", 1140, 15),
		effort("10k", 2370, 10),
	}
	in := EstimateInput{
		TargetDistanceKey:    "half_marathon",
		TargetDistanceMeters: distMeters["half_marathon"],
		Attempts:             attempts,
		Now:                  refNow,
	}
	r := est.Estimate(in)
	if r.Basis != "fitted_curve" {
		t.Fatalf("Basis = %q, want fitted_curve", r.Basis)
	}

	// Recover the fitted exponent by re-running the math is overkill; instead
	// assert the prediction is sane and that the band is narrower than the
	// single-effort regime (compared below).
	if r.Seconds <= 0 {
		t.Fatalf("Seconds = %v, want > 0", r.Seconds)
	}

	// The fitted exponent should move toward the data slope, away from 1.06.
	exp := fittedExponent(t, in)
	if math.Abs(exp-priorBeta1Mean) < 1e-4 {
		t.Errorf("fitted exponent %.4f did not move off the prior 1.06", exp)
	}
	t.Logf("multi-distance fitted exponent = %.4f", exp)

	// Band narrower than a single 10k effort projected to the same target.
	single := est.Estimate(EstimateInput{
		TargetDistanceKey:    "half_marathon",
		TargetDistanceMeters: distMeters["half_marathon"],
		Attempts:             []Attempt{effort("10k", 2370, 10)},
		Now:                  refNow,
	})
	if halfWidth(r) >= halfWidth(single) {
		t.Errorf("multi-distance half-width %.3f not narrower than single %.3f", halfWidth(r), halfWidth(single))
	}
}

func TestEstimate_ThinFastShortEffortStaysConservative(t *testing.T) {
	est := NewEstimator()
	// One blistering 1-mile (4:30) and nothing else. Projecting to marathon
	// must NOT extrapolate to an absurd time — the slope stays near the prior.
	in := EstimateInput{
		TargetDistanceKey:    "marathon",
		TargetDistanceMeters: distMeters["marathon"],
		Attempts:             []Attempt{effort("1mi", 270, 5)},
		Now:                  refNow,
	}
	r := est.Estimate(in)

	// Riegel from a 4:30 mile lands the marathon near ~2:30; a runaway model
	// would predict far faster. Assert it stays at or above a sane floor.
	if r.Seconds < 8000 { // 8000s ≈ 2:13 — anything faster is implausible
		t.Errorf("marathon estimate %.0fs is implausibly fast for a lone 4:30 mile", r.Seconds)
	}
	want := riegelProject(270, distMeters["1mi"], distMeters["marathon"], priorBeta1Mean)
	relErr := math.Abs(r.Seconds-want) / want
	if relErr > 0.05 {
		t.Errorf("Seconds = %.0f strayed from Riegel projection %.0f (rel %.3f)", r.Seconds, want, relErr)
	}
}

func TestEstimate_RecentEffortDominatesOld(t *testing.T) {
	est := NewEstimator()
	// Same 5k distance: an old fast effort (18:00, 2 years ago) and a recent
	// slower one (21:00, last week). The recent one should pull the level.
	in := EstimateInput{
		TargetDistanceKey:    "5k",
		TargetDistanceMeters: distMeters["5k"],
		Attempts: []Attempt{
			effort("5k", 1080, 730), // old & fast
			effort("5k", 1260, 7),   // recent & slower
		},
		Now: refNow,
	}
	r := est.Estimate(in)
	// The estimate should sit closer to the recent 21:00 than the old 18:00.
	distToRecent := math.Abs(r.Seconds - 1260)
	distToOld := math.Abs(r.Seconds - 1080)
	if distToRecent >= distToOld {
		t.Errorf("estimate %.0f is not closer to recent 1260 (|%.0f|) than old 1080 (|%.0f|)",
			r.Seconds, distToRecent, distToOld)
	}
}

func TestEstimate_LowQualityWindowPullsLess(t *testing.T) {
	est := NewEstimator()
	base := []Attempt{effort("10k", 2400, 30)} // a real 10k anchor, 40:00

	// A 5k effort that is a small window of a 15k run vs the same effort from
	// a ~5k activity. The low-quality version should move the estimate less.
	highQ := Attempt{
		DistanceKey: "5k", DistanceMeters: 5000, DurationSeconds: 1080,
		AchievedAt: refNow.Add(-5 * 24 * time.Hour), ActivityDistanceMeters: 5200,
	}
	lowQ := highQ
	lowQ.ActivityDistanceMeters = 15000

	rHigh := est.Estimate(EstimateInput{
		TargetDistanceKey: "5k", TargetDistanceMeters: 5000,
		Attempts: append(append([]Attempt{}, base...), highQ), Now: refNow,
	})
	rLow := est.Estimate(EstimateInput{
		TargetDistanceKey: "5k", TargetDistanceMeters: 5000,
		Attempts: append(append([]Attempt{}, base...), lowQ), Now: refNow,
	})
	rBase := est.Estimate(EstimateInput{
		TargetDistanceKey: "5k", TargetDistanceMeters: 5000,
		Attempts: base, Now: refNow,
	})
	// The fast 5k pulls the estimate down from the 10k-only baseline. The
	// low-quality version, being down-weighted, must pull it down less.
	moveHigh := rBase.Seconds - rHigh.Seconds
	moveLow := rBase.Seconds - rLow.Seconds
	if !(moveHigh > moveLow) {
		t.Errorf("expected high-quality to move estimate more: moveHigh=%.2f moveLow=%.2f", moveHigh, moveLow)
	}
}

func TestEstimate_BandNarrowsAsDataAdded(t *testing.T) {
	est := NewEstimator()
	one := est.Estimate(EstimateInput{
		TargetDistanceKey: "half_marathon", TargetDistanceMeters: distMeters["half_marathon"],
		Attempts: []Attempt{effort("5k", 1140, 10)}, Now: refNow,
	})
	two := est.Estimate(EstimateInput{
		TargetDistanceKey: "half_marathon", TargetDistanceMeters: distMeters["half_marathon"],
		Attempts: []Attempt{effort("1mi", 330, 12), effort("5k", 1140, 10)}, Now: refNow,
	})
	three := est.Estimate(EstimateInput{
		TargetDistanceKey: "half_marathon", TargetDistanceMeters: distMeters["half_marathon"],
		Attempts: []Attempt{effort("1mi", 330, 12), effort("5k", 1140, 10), effort("10k", 2370, 8)}, Now: refNow,
	})
	h1, h2, h3 := halfWidth(one), halfWidth(two), halfWidth(three)
	if !(h1 > h2 && h2 > h3) {
		t.Errorf("band did not narrow monotonically: %.3f -> %.3f -> %.3f", h1, h2, h3)
	}
}

func TestEstimate_Deterministic(t *testing.T) {
	est := NewEstimator()
	in := EstimateInput{
		TargetDistanceKey: "10k", TargetDistanceMeters: distMeters["10k"],
		Attempts:     []Attempt{effort("1mi", 330, 12), effort("5k", 1140, 10)},
		Demographics: Demographics{Age: intPtr(28), Sex: strPtr("female")},
		Now:          refNow,
	}
	first := est.Estimate(in)
	for i := 0; i < 5; i++ {
		got := est.Estimate(in)
		if got != first {
			t.Fatalf("non-deterministic result on call %d: %+v != %+v", i, got, first)
		}
	}
}

// halfWidth is the relative half-width of a result's band, the quantity that
// drives the confidence label.
func halfWidth(r EstimateResult) float64 {
	if r.Seconds <= 0 {
		return 0
	}
	return (r.UpperSeconds - r.LowerSeconds) / (2 * r.Seconds)
}

// fittedExponent recovers m_N[1] (the posterior slope) for an input by
// replaying the same closed-form math the model uses, so tests can assert on
// the fitted exponent directly.
func fittedExponent(t *testing.T, in EstimateInput) float64 {
	t.Helper()
	used := make([]usableAttempt, 0, len(in.Attempts))
	for _, a := range in.Attempts {
		if a.DurationSeconds <= 0 || a.DistanceMeters <= 0 {
			continue
		}
		w := attemptWeight(in.Now, a)
		used = append(used, usableAttempt{x: math.Log(a.DistanceMeters), y: math.Log(a.DurationSeconds), weight: w})
	}
	dm0, _, ok := demographicLevelPrior(in.Demographics)
	var mBeta0, s0Beta0 float64
	if ok {
		mBeta0, s0Beta0 = dm0, priorBeta0Var
	} else {
		s0Beta0 = diffuseBeta0Var
		xFast, yFast := fastestEffort(used)
		mBeta0 = yFast - priorBeta1Mean*xFast
	}
	s0invBeta0 := 1.0 / s0Beta0
	s0invBeta1 := 1.0 / priorBeta1Var
	a00, a01, a11 := s0invBeta0, 0.0, s0invBeta1
	b0, b1 := s0invBeta0*mBeta0, s0invBeta1*priorBeta1Mean
	for _, u := range used {
		p := u.weight / obsVar
		a00 += p
		a01 += p * u.x
		a11 += p * u.x * u.x
		b0 += p * u.y
		b1 += p * u.x * u.y
	}
	det := a00*a11 - a01*a01
	s01 := -a01 / det
	s11 := a00 / det
	return s01*b0 + s11*b1
}
