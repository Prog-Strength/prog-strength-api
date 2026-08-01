package dashboard

import (
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

func bike(name string, dist float64, dur int, start time.Time) activity.Activity {
	a := activity.Activity{
		ActivityType:    activity.ActivityCycling,
		StartTime:       start,
		DistanceMeters:  dist,
		DurationSeconds: dur,
	}
	if name != "" {
		a.Name = ptrS(name)
	}
	return a
}

func TestBuildCycling_EmptyReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	if got := buildCycling(nil, now, denver); got != nil {
		t.Errorf("no sessions should be nil, got %+v", got)
	}
}

func TestBuildCycling_OnlyOtherTypesReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	sessions := []activity.Activity{
		run("Run", 5000, 1500, time.Date(2026, 6, 16, 7, 0, 0, 0, denver)),
		walk("Walk", 3000, 1800, time.Date(2026, 6, 16, 8, 0, 0, 0, denver)),
	}
	if got := buildCycling(sessions, now, denver); got != nil {
		t.Errorf("only other types should be nil, got %+v", got)
	}
}

func TestBuildCycling_CurrentWeekPassThrough(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	sessions := []activity.Activity{
		bike("a", 20000, 3600, time.Date(2026, 6, 15, 6, 0, 0, 0, denver)),
		bike("b", 15000, 2700, time.Date(2026, 6, 17, 6, 0, 0, 0, denver)),
		bike("c", 40000, 7200, time.Date(2026, 6, 10, 6, 0, 0, 0, denver)), // last week, excluded
	}

	got := buildCycling(sessions, now, denver)
	if got == nil {
		t.Fatal("expected section")
	}
	if got.CurrentWeek.DistanceMeters != 35000 {
		t.Errorf("current week distance = %v, want 35000", got.CurrentWeek.DistanceMeters)
	}
	if got.CurrentWeek.SessionCount != 2 {
		t.Errorf("current week count = %d, want 2", got.CurrentWeek.SessionCount)
	}
	if got.CurrentWeek.DurationSeconds != 6300 {
		t.Errorf("current week duration = %d, want 6300", got.CurrentWeek.DurationSeconds)
	}
}

func TestBuildCycling_LatestIsMaxStartTime(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	sessions := []activity.Activity{
		bike("Morning", 20000, 3600, time.Date(2026, 6, 16, 7, 0, 0, 0, denver)),
		bike("Newest", 25000, 4000, time.Date(2026, 6, 18, 12, 0, 0, 0, denver)),
		bike("Old", 10000, 1800, time.Date(2026, 6, 1, 8, 0, 0, 0, denver)),
	}

	got := buildCycling(sessions, now, denver)
	if got.LatestSession == nil || got.LatestSession.Name == nil || *got.LatestSession.Name != "Newest" {
		t.Fatalf("latest mismatch: %+v", got.LatestSession)
	}
	if got.LatestSession.DistanceMeters != 25000 || got.LatestSession.DurationSeconds != 4000 {
		t.Errorf("latest fields mismatch: %+v", got.LatestSession)
	}
}

func TestBuildCycling_WeeklySparkZeroFilledAndLocalBucketing(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	// Buckets (Mondays): 04-27,05-04,05-11,05-18,05-25,06-01,06-08,06-15
	sessions := []activity.Activity{
		bike("a", 20000, 0, time.Date(2026, 6, 15, 6, 0, 0, 0, denver)),
		bike("b", 15000, 0, time.Date(2026, 6, 17, 6, 0, 0, 0, denver)),
		bike("c", 30000, 0, time.Date(2026, 6, 3, 6, 0, 0, 0, denver)), // week 06-01
		bike("d", 99999, 0, time.Date(2026, 4, 1, 6, 0, 0, 0, denver)), // too old
		// 2026-06-15 05:00 UTC = Sunday 23:00 Denver = 06-08 Denver week.
		bike("e", 12000, 0, time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC)),
	}

	got := buildCycling(sessions, now, denver)
	if len(got.WeeklyDistanceSpark) != sparkWeeks {
		t.Errorf("spark length = %d, want %d", len(got.WeeklyDistanceSpark), sparkWeeks)
	}
	want := []float64{0, 0, 0, 0, 0, 30000, 12000, 35000}
	if !reflect.DeepEqual(got.WeeklyDistanceSpark, want) {
		t.Errorf("spark = %v, want %v", got.WeeklyDistanceSpark, want)
	}
}
