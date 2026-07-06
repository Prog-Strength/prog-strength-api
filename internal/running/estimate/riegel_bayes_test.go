package estimate

import (
	"math"
	"testing"
	"time"
)

func TestRecencyWeight_DecayOrdering(t *testing.T) {
	now := refNow
	wNew := recencyWeight(now, now)
	wMid := recencyWeight(now, now.Add(-90*24*time.Hour))
	wOld := recencyWeight(now, now.Add(-365*24*time.Hour))

	if math.Abs(wNew-1.0) > 1e-9 {
		t.Errorf("fresh effort weight = %v, want 1.0", wNew)
	}
	if !(wNew > wMid && wMid > wOld) {
		t.Errorf("recency weights not strictly decreasing: %v, %v, %v", wNew, wMid, wOld)
	}
	// At exactly tauDays the weight should be 1/e.
	wTau := recencyWeight(now, now.Add(-time.Duration(tauDays)*24*time.Hour))
	if math.Abs(wTau-math.Exp(-1)) > 1e-6 {
		t.Errorf("weight at tauDays = %v, want %v", wTau, math.Exp(-1))
	}
	// A future-dated effort is clamped to "now", not amplified.
	wFuture := recencyWeight(now, now.Add(48*time.Hour))
	if math.Abs(wFuture-1.0) > 1e-9 {
		t.Errorf("future effort weight = %v, want clamped 1.0", wFuture)
	}
}

func TestQualityWeight_FloorAndKnee(t *testing.T) {
	// Unknown activity distance → full weight.
	if w := qualityWeight(5000, 0); w != 1.0 {
		t.Errorf("unknown activity weight = %v, want 1.0", w)
	}
	// At/above the knee → full weight.
	if w := qualityWeight(5000, 5000); math.Abs(w-1.0) > 1e-9 {
		t.Errorf("race-like (ratio 1.0) weight = %v, want 1.0", w)
	}
	if w := qualityWeight(4500, 5000); math.Abs(w-1.0) > 1e-9 { // ratio 0.9 == knee
		t.Errorf("ratio-at-knee weight = %v, want 1.0", w)
	}
	// Tiny window of a long run → near the floor, never below it.
	wTiny := qualityWeight(5000, 50000) // ratio 0.1
	if wTiny < qualityFloor {
		t.Errorf("tiny-window weight %v fell below floor %v", wTiny, qualityFloor)
	}
	// Monotonic: bigger share of the run earns more weight.
	if !(qualityWeight(2000, 10000) < qualityWeight(6000, 10000)) {
		t.Errorf("quality weight not increasing with effort share")
	}
	// A vanishing share approaches exactly the floor.
	wZero := qualityWeight(1, 1e9)
	if math.Abs(wZero-qualityFloor) > 1e-3 {
		t.Errorf("vanishing-share weight = %v, want ~floor %v", wZero, qualityFloor)
	}
}

func TestClassifySource(t *testing.T) {
	cases := []struct {
		effort, activity float64
		want             string
	}{
		{5000, 0, "race_like"},    // unknown activity
		{5000, 5000, "race_like"}, // fills the run
		{4500, 5000, "race_like"}, // exactly at the knee (0.9)
		{5000, 15000, "long_run_window"},
	}
	for _, c := range cases {
		if got := ClassifySource(c.effort, c.activity); got != c.want {
			t.Errorf("ClassifySource(%v, %v) = %q, want %q", c.effort, c.activity, got, c.want)
		}
	}
}

func TestDemographicLevelPrior_RequiresAgeAndSex(t *testing.T) {
	cases := []struct {
		name string
		d    Demographics
		want bool
	}{
		{"empty", Demographics{}, false},
		{"age only", Demographics{Age: intPtr(30)}, false},
		{"sex only", Demographics{Sex: strPtr("male")}, true},
		{"height only", Demographics{HeightCm: f64Ptr(180)}, false},
		{"age+sex", Demographics{Age: intPtr(30), Sex: strPtr("male")}, true},
		{"age+sex+height", Demographics{Age: intPtr(30), Sex: strPtr("male"), HeightCm: f64Ptr(180)}, true},
	}
	for _, c := range cases {
		_, variance, ok := demographicLevelPrior(c.d)
		if ok != c.want {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.want)
		}
		if ok && variance != priorBeta0Var {
			t.Errorf("%s: variance = %v, want %v", c.name, variance, priorBeta0Var)
		}
	}
}

func TestDemographicLevelPrior_RecoversStandardTime(t *testing.T) {
	// With age==refAgeYears and male sex the standard is exactly the base 5k.
	d := Demographics{Age: intPtr(30), Sex: strPtr("male")}
	mBeta0, _, ok := demographicLevelPrior(d)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// Reconstruct the implied 5k time from the prior line and check it matches.
	xRef := math.Log(refDistanceMeters)
	tStd := math.Exp(mBeta0 + priorBeta1Mean*xRef)
	wantMale := male5kForAge(30)
	if math.Abs(tStd-wantMale) > 1e-6 {
		t.Errorf("recovered 5k standard = %.3f, want %.3f", tStd, wantMale)
	}

	// Female standard should imply a slower (larger) time than male.
	mFemale, _, _ := demographicLevelPrior(Demographics{Age: intPtr(30), Sex: strPtr("female")})
	tFemale := math.Exp(mFemale + priorBeta1Mean*xRef)
	if !(tFemale > tStd) {
		t.Errorf("female standard %.1f not slower than male %.1f", tFemale, tStd)
	}
}
