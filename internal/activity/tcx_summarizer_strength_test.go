package activity

import (
	"errors"
	"testing"
)

// summarizeStrengthFixture parses a fixture and runs the strength validate +
// summarize path, returning the summary. It fails the test on parse/validate
// errors so the happy-path cases stay terse.
func summarizeStrengthFixture(t *testing.T, name string) Activity {
	t.Helper()
	p, err := parseTCX(readFixture(t, name))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if err := validateStrength(p); err != nil {
		t.Fatalf("validateStrength %s: %v", name, err)
	}
	return summarizeStrength(p)
}

func TestSummarizeStrength_Session(t *testing.T) {
	a := summarizeStrengthFixture(t, "strength_session.tcx")

	if a.ActivityType != ActivityStrengthTraining {
		t.Errorf("ActivityType = %q, want %q", a.ActivityType, ActivityStrengthTraining)
	}
	// HR samples 100/120/140/160/180 => mean exactly 140, max 180.
	if a.AvgHeartRateBpm == nil || *a.AvgHeartRateBpm != 140 {
		t.Errorf("AvgHeartRateBpm = %v, want 140", a.AvgHeartRateBpm)
	}
	if a.MaxHeartRateBpm == nil || *a.MaxHeartRateBpm != 180 {
		t.Errorf("MaxHeartRateBpm = %v, want 180", a.MaxHeartRateBpm)
	}
	// Single lap, 240 kcal.
	if a.TotalCalories == nil || *a.TotalCalories != 240 {
		t.Errorf("TotalCalories = %v, want 240", a.TotalCalories)
	}
	// First tp 13:12:00 -> last 13:14:00 = 120 s (±1).
	if a.DurationSeconds < 119 || a.DurationSeconds > 121 {
		t.Errorf("DurationSeconds = %d, want ~120", a.DurationSeconds)
	}
	// Distance-free: no distance, pace, elevation, or best efforts.
	if a.DistanceMeters != 0 {
		t.Errorf("DistanceMeters = %v, want 0", a.DistanceMeters)
	}
	if a.AvgPaceSecPerKm != nil || a.BestPaceSecPerKm != nil {
		t.Errorf("paces = %v/%v, want nil", a.AvgPaceSecPerKm, a.BestPaceSecPerKm)
	}
	if a.ElevationGainMeters != nil {
		t.Errorf("ElevationGainMeters = %v, want nil", a.ElevationGainMeters)
	}
	if len(a.BestEfforts) != 0 {
		t.Errorf("BestEfforts = %v, want empty", a.BestEfforts)
	}
	// StartTime is the first trackpoint's timestamp (13:12:00Z by fixture).
	wantStart := "2026-06-19T13:12:00Z"
	if got := a.StartTime.UTC().Format("2006-01-02T15:04:05Z"); got != wantStart {
		t.Errorf("StartTime = %s, want %s", got, wantStart)
	}
	// Trackpoints carry only elapsed + HR; distance forced to 0.
	if len(a.Trackpoints) == 0 {
		t.Fatal("Trackpoints empty, want downsampled points")
	}
	for i, tp := range a.Trackpoints {
		if tp.DistanceMeters != 0 {
			t.Errorf("trackpoint[%d].DistanceMeters = %v, want 0", i, tp.DistanceMeters)
		}
		if tp.PaceSecPerKm != nil || tp.ElevationMeters != nil {
			t.Errorf("trackpoint[%d] pace/elev = %v/%v, want nil", i, tp.PaceSecPerKm, tp.ElevationMeters)
		}
	}
	if a.Trackpoints[0].ElapsedSeconds != 0 {
		t.Errorf("first elapsed = %d, want 0", a.Trackpoints[0].ElapsedSeconds)
	}
}

func TestSummarizeStrength_NoHR(t *testing.T) {
	a := summarizeStrengthFixture(t, "strength_no_hr.tcx")

	if a.AvgHeartRateBpm != nil || a.MaxHeartRateBpm != nil {
		t.Errorf("HR = %v/%v, want nil (no samples)", a.AvgHeartRateBpm, a.MaxHeartRateBpm)
	}
	// Calories present, so the file still summarizes.
	if a.TotalCalories == nil || *a.TotalCalories != 200 {
		t.Errorf("TotalCalories = %v, want 200", a.TotalCalories)
	}
	if a.DurationSeconds < 59 || a.DurationSeconds > 61 {
		t.Errorf("DurationSeconds = %d, want ~60", a.DurationSeconds)
	}
}

func TestValidateStrength_NoEffortData(t *testing.T) {
	p, err := parseTCX(readFixture(t, "no_effort_data.tcx"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = validateStrength(p)
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("validateStrength err = %v, want *ValidationError", err)
	}
	if verr.Slug != SlugNoEffortData {
		t.Errorf("slug = %q, want %q", verr.Slug, SlugNoEffortData)
	}
}

func TestValidateStrength_Malformed(t *testing.T) {
	// parseTCX rejects malformed XML before validation runs; the ingest seam
	// wraps that as SlugParseFailed. Here we just assert parse fails cleanly
	// (no panic).
	_, err := parseTCX(readFixture(t, "malformed.tcx"))
	if err == nil {
		t.Fatal("parse malformed.tcx = nil error, want parse failure")
	}
}

// TestSummarizeStrength_RunFileKeepsHROnly documents the "attached a run by
// mistake" behavior: a running TCX summarized through the strength path yields
// HR/calories but discards distance/pace/elevation entirely.
func TestSummarizeStrength_RunFileKeepsHROnly(t *testing.T) {
	a := summarizeStrengthFixture(t, "biking.tcx")

	if a.ActivityType != ActivityStrengthTraining {
		t.Errorf("ActivityType = %q, want %q", a.ActivityType, ActivityStrengthTraining)
	}
	// biking.tcx carries HR (120/121/122) and 5 kcal — both survive.
	if a.AvgHeartRateBpm == nil {
		t.Error("AvgHeartRateBpm = nil, want HR from the file")
	}
	if a.TotalCalories == nil || *a.TotalCalories != 5 {
		t.Errorf("TotalCalories = %v, want 5", a.TotalCalories)
	}
	// Distance is discarded even though the file has it.
	if a.DistanceMeters != 0 {
		t.Errorf("DistanceMeters = %v, want 0 (distance discarded)", a.DistanceMeters)
	}
	for i, tp := range a.Trackpoints {
		if tp.DistanceMeters != 0 {
			t.Errorf("trackpoint[%d].DistanceMeters = %v, want 0", i, tp.DistanceMeters)
		}
	}
}
