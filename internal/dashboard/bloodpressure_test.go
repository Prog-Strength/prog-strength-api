package dashboard

import (
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bloodpressure"
)

// bpNow is a fixed reference instant so the trailing-30-day window is
// deterministic regardless of when the suite runs.
var bpNow = time.Date(2026, 6, 17, 13, 0, 0, 0, time.UTC)

func bpEntry(sys, dia int, at time.Time) bloodpressure.Entry {
	return bloodpressure.Entry{Systolic: sys, Diastolic: dia, MeasuredAt: at}
}

func TestBuildBloodPressure_NilOnEmpty(t *testing.T) {
	if got := buildBloodPressure(nil, bpNow, time.UTC); got != nil {
		t.Fatalf("got %+v, want nil for empty input", got)
	}
	if got := buildBloodPressure([]bloodpressure.Entry{}, bpNow, time.UTC); got != nil {
		t.Fatalf("got %+v, want nil for empty input", got)
	}
}

func TestBuildBloodPressure_SparksDayAlignedAndEqualLength(t *testing.T) {
	day1 := time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC)
	entries := []bloodpressure.Entry{
		bpEntry(120, 80, day1.Add(1*time.Hour)),
		bpEntry(122, 82, day1.Add(2*time.Hour)),
		bpEntry(124, 84, day1.Add(3*time.Hour)),
		bpEntry(130, 85, day2),
	}
	got := buildBloodPressure(entries, bpNow, time.UTC)
	if got == nil {
		t.Fatal("got nil, want section")
	}
	if len(got.SystolicSpark) != 2 {
		t.Errorf("systolic spark len = %d, want 2 (two distinct local days)", len(got.SystolicSpark))
	}
	if len(got.SystolicSpark) != len(got.DiastolicSpark) {
		t.Errorf("spark lengths differ: sys=%d dia=%d", len(got.SystolicSpark), len(got.DiastolicSpark))
	}
	// day1 systolic mean = (120+122+124)/3 = 122; day2 = 130.
	if got.SystolicSpark[0] != 122 || got.SystolicSpark[1] != 130 {
		t.Errorf("systolic spark = %v, want [122 130]", got.SystolicSpark)
	}
	// day1 diastolic mean = (80+82+84)/3 = 82; day2 = 85.
	if got.DiastolicSpark[0] != 82 || got.DiastolicSpark[1] != 85 {
		t.Errorf("diastolic spark = %v, want [82 85]", got.DiastolicSpark)
	}
}

func TestBuildBloodPressure_Avg30ExcludesOldReadings(t *testing.T) {
	old := bpNow.AddDate(0, 0, -40)
	recent := bpNow.AddDate(0, 0, -2)
	entries := []bloodpressure.Entry{
		bpEntry(200, 130, old), // 40 days old — excluded from avg30
		bpEntry(118, 76, recent),
	}
	got := buildBloodPressure(entries, bpNow, time.UTC)
	if got == nil || got.Avg30 == nil {
		t.Fatalf("got %+v, want non-nil section + avg30", got)
	}
	if got.Avg30.Systolic != 118 || got.Avg30.Diastolic != 76 {
		t.Errorf("avg30 = %+v, want {118 76} (older reading excluded)", *got.Avg30)
	}
}

func TestBuildBloodPressure_CategoryIsNewest(t *testing.T) {
	older := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	// older is normal (110/70); newer is stage_2 (145/95).
	entries := []bloodpressure.Entry{
		bpEntry(110, 70, older),
		bpEntry(145, 95, newer),
	}
	got := buildBloodPressure(entries, bpNow, time.UTC)
	if got == nil {
		t.Fatal("got nil, want section")
	}
	if got.Category != bloodpressure.CategoryStage2 {
		t.Errorf("category = %q, want %q (newest reading's category)", got.Category, bloodpressure.CategoryStage2)
	}
	if got.Latest.Systolic != 145 || got.Latest.Diastolic != 95 {
		t.Errorf("latest = %+v, want systolic 145 diastolic 95", got.Latest)
	}
}

func TestBuildBloodPressure_DailyAverageRounding(t *testing.T) {
	day := time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC)
	// mean systolic = (120+121)/2 = 120.5 → math.Round rounds half away from
	// zero → 121.
	entries := []bloodpressure.Entry{
		bpEntry(120, 80, day.Add(1*time.Hour)),
		bpEntry(121, 81, day.Add(2*time.Hour)),
	}
	got := buildBloodPressure(entries, bpNow, time.UTC)
	if got == nil {
		t.Fatal("got nil, want section")
	}
	if len(got.SystolicSpark) != 1 {
		t.Fatalf("systolic spark len = %d, want 1", len(got.SystolicSpark))
	}
	if got.SystolicSpark[0] != 121 {
		t.Errorf("systolic daily mean = %v, want 121 (math.Round(120.5))", got.SystolicSpark[0])
	}
	// diastolic mean = (80+81)/2 = 80.5 → 81.
	if got.DiastolicSpark[0] != 81 {
		t.Errorf("diastolic daily mean = %v, want 81 (math.Round(80.5))", got.DiastolicSpark[0])
	}
}

func TestBuildBloodPressure_LocalDayBucketing(t *testing.T) {
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	// A reading at 23:30 on June 15 local (EDT, UTC-4) is 03:30 June 16 in UTC.
	// Bucketed by local day it must land on 2026-06-15, distinct from its UTC
	// calendar day 2026-06-16.
	local2330 := time.Date(2026, 6, 15, 23, 30, 0, 0, nyc)
	// A second reading squarely on June 16 local.
	next := time.Date(2026, 6, 16, 9, 0, 0, 0, nyc)
	entries := []bloodpressure.Entry{
		bpEntry(120, 80, local2330),
		bpEntry(140, 90, next),
	}
	got := buildBloodPressure(entries, bpNow, nyc)
	if got == nil {
		t.Fatal("got nil, want section")
	}
	// Two distinct local days → two spark points.
	if len(got.SystolicSpark) != 2 {
		t.Errorf("systolic spark len = %d, want 2 (two distinct local days)", len(got.SystolicSpark))
	}
	// Sanity: if bucketed by UTC day these would still be two days, so also
	// verify the first point is the 23:30 reading's value (120), proving it
	// bucketed as the earlier local day.
	if got.SystolicSpark[0] != 120 {
		t.Errorf("first spark point = %v, want 120 (June-15-local reading sorts first)", got.SystolicSpark[0])
	}
}
