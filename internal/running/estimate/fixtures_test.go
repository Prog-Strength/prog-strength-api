package estimate

import (
	"math"
	"testing"
	"time"
)

// Owner5KMismatch reproduces the shipped v1 failure: many slower historical
// 5K rows plus one true best at 23:06 — v2 must estimate ≤ logged best.
func TestFixture_Owner5KMismatch(t *testing.T) {
	now := refNow
	best := 1386.0 // 23:06
	attempts := []Attempt{
		anchorAttempt("5k", 5000, best, now.Add(-30*24*time.Hour), 5100, racePace(5000, best)),
		supportAttempt("5k", 5000, 1679, now.Add(-120*24*time.Hour), 15000, f64Ptr(easyPace)), // 27:59
		supportAttempt("5k", 5000, 1611, now.Add(-90*24*time.Hour), 15000, f64Ptr(easyPace)),  // 26:51
		supportAttempt("5k", 5000, 1540, now.Add(-60*24*time.Hour), 15000, f64Ptr(easyPace)),  // 25:40
		supportAttempt("5k", 5000, 1660, now.Add(-45*24*time.Hour), 15000, f64Ptr(easyPace)),  // 27:40
	}
	logged := best
	res := NewEstimator().Estimate(EstimateInput{
		TargetDistanceKey: "5k", TargetDistanceMeters: 5000,
		Attempts: attempts, Now: now, LoggedBestSeconds: &logged,
	})
	assertEstimateAtMostLoggedBest(t, res, logged)
	if res.Version != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", res.Version)
	}
}

func TestFixture_MileFloor(t *testing.T) {
	now := refNow
	best := 440.0 // 7:20
	mile := 1609.344
	attempts := []Attempt{
		anchorAttempt("1mi", mile, best, now.Add(-20*24*time.Hour), mile, racePace(mile, best)),
		supportAttempt("1mi", mile, 488, now.Add(-100*24*time.Hour), 10000, f64Ptr(easyPace)), // 8:08 window
	}
	logged := best
	res := NewEstimator().Estimate(EstimateInput{
		TargetDistanceKey: "1mi", TargetDistanceMeters: mile,
		Attempts: attempts, Now: now, LoggedBestSeconds: &logged,
	})
	assertEstimateAtMostLoggedBest(t, res, logged)
}

func TestIncludeSupportingHistory_ExcludesStalePRs(t *testing.T) {
	anchor := 1386.0
	stale := 1679.0 // ~21% slower
	if IncludeSupportingHistory(anchor, stale) {
		t.Error("expected stale PR to be excluded")
	}
	near := anchor * 1.02
	if !IncludeSupportingHistory(anchor, near) {
		t.Error("expected near-miss within 3% to be included")
	}
}

func TestLoggedBestFloor_Clamp(t *testing.T) {
	slow := 2000.0
	logged := 1386.0
	res := applyLoggedBestFloor(EstimateResult{
		Seconds: slow, LowerSeconds: 1900, UpperSeconds: 2100,
		RawSeconds: slow, Basis: "fitted_curve", Confidence: "high",
	}, &logged)
	if res.Seconds > logged+1e-6 {
		t.Errorf("seconds = %v, want <= %v", res.Seconds, logged)
	}
	if !res.FlooredAtLoggedBest {
		t.Error("expected floored flag")
	}
	if res.Basis != "logged_best_floor" {
		t.Errorf("basis = %q", res.Basis)
	}
}

func assertEstimateAtMostLoggedBest(t *testing.T, res EstimateResult, logged float64) {
	t.Helper()
	if res.Basis == "insufficient_data" {
		t.Fatal("insufficient_data")
	}
	if res.Seconds > logged+1e-6 {
		t.Errorf("estimate %v slower than logged best %v (raw %v)", res.Seconds, logged, res.RawSeconds)
	}
}

const easyPace = 360.0 // 6:00/km

func racePace(meters, seconds float64) *float64 {
	p := seconds / (meters / 1000)
	return &p
}

func anchorAttempt(key string, meters, seconds float64, at time.Time, actMeters float64, pace *float64) Attempt {
	return Attempt{
		DistanceKey: key, DistanceMeters: meters, DurationSeconds: seconds,
		AchievedAt: at, ActivityDistanceMeters: actMeters,
		ActivityAvgPaceSecPerKm: pace, IsCurrentBestAtDistance: true,
	}
}

func supportAttempt(key string, meters, seconds float64, at time.Time, actMeters float64, pace *float64) Attempt {
	return Attempt{
		DistanceKey: key, DistanceMeters: meters, DurationSeconds: seconds,
		AchievedAt: at, ActivityDistanceMeters: actMeters,
		ActivityAvgPaceSecPerKm: pace,
	}
}

func TestPaceRatioWeight_FasterWindowFullWeight(t *testing.T) {
	pace := 300.0
	w := paceRatioWeight(5000, 1500, &pace) // window 300 s/km == activity avg
	if math.Abs(w-1.0) > 1e-9 {
		t.Errorf("weight = %v, want 1.0", w)
	}
}
