package dashboard

import (
	"reflect"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
)

func walk(name string, dist float64, dur int, start time.Time) activity.Activity {
	a := activity.Activity{
		ActivityType:    activity.ActivityWalking,
		StartTime:       start,
		DistanceMeters:  dist,
		DurationSeconds: dur,
	}
	if name != "" {
		a.Name = ptrS(name)
	}
	return a
}

func TestBuildWalking_EmptyReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	if got := buildWalking(nil, now, denver); got != nil {
		t.Errorf("no sessions should be nil, got %+v", got)
	}
}

func TestBuildWalking_OnlyOtherTypesReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	// A run and a bike, but no walk: nil-on-empty guard is per-type.
	sessions := []activity.Activity{
		run("Run", 5000, 1500, time.Date(2026, 6, 16, 7, 0, 0, 0, denver)),
		{ActivityType: activity.ActivityCycling, StartTime: time.Date(2026, 6, 16, 8, 0, 0, 0, denver), DistanceMeters: 20000},
	}
	if got := buildWalking(sessions, now, denver); got != nil {
		t.Errorf("only other types should be nil, got %+v", got)
	}
}

func TestBuildWalking_CurrentWeekPassThrough(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver) // Wed; current week Mon = 06-15

	sessions := []activity.Activity{
		// Current week (06-15): two walks sum.
		walk("a", 3000, 1800, time.Date(2026, 6, 15, 6, 0, 0, 0, denver)),
		walk("b", 4000, 2400, time.Date(2026, 6, 17, 6, 0, 0, 0, denver)),
		// Last week (06-08): excluded from current week.
		walk("c", 9000, 5000, time.Date(2026, 6, 10, 6, 0, 0, 0, denver)),
	}

	got := buildWalking(sessions, now, denver)
	if got == nil {
		t.Fatal("expected section")
	}
	if got.CurrentWeek.DistanceMeters != 7000 {
		t.Errorf("current week distance = %v, want 7000", got.CurrentWeek.DistanceMeters)
	}
	if got.CurrentWeek.SessionCount != 2 {
		t.Errorf("current week count = %d, want 2", got.CurrentWeek.SessionCount)
	}
	if got.CurrentWeek.DurationSeconds != 4200 {
		t.Errorf("current week duration = %d, want 4200", got.CurrentWeek.DurationSeconds)
	}
}

func TestBuildWalking_LatestIsMaxStartTime(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	// Out-of-order to prove max-StartTime selection.
	sessions := []activity.Activity{
		walk("Morning", 3000, 1800, time.Date(2026, 6, 16, 7, 0, 0, 0, denver)),
		walk("Newest", 4200, 2400, time.Date(2026, 6, 18, 12, 0, 0, 0, denver)),
		walk("Old", 2000, 1200, time.Date(2026, 6, 1, 8, 0, 0, 0, denver)),
	}

	got := buildWalking(sessions, now, denver)
	if got.LatestSession == nil || got.LatestSession.Name == nil || *got.LatestSession.Name != "Newest" {
		t.Fatalf("latest mismatch: %+v", got.LatestSession)
	}
	if got.LatestSession.DistanceMeters != 4200 || got.LatestSession.DurationSeconds != 2400 {
		t.Errorf("latest fields mismatch: %+v", got.LatestSession)
	}
}

func TestBuildWalking_WeeklySparkZeroFilledAndLocalBucketing(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver) // Wed; current week Mon = 06-15

	// Buckets (Mondays): 04-27,05-04,05-11,05-18,05-25,06-01,06-08,06-15
	sessions := []activity.Activity{
		walk("a", 3000, 0, time.Date(2026, 6, 15, 6, 0, 0, 0, denver)),
		walk("b", 4000, 0, time.Date(2026, 6, 17, 6, 0, 0, 0, denver)),
		walk("c", 5000, 0, time.Date(2026, 6, 3, 6, 0, 0, 0, denver)), // week 06-01
		walk("d", 9999, 0, time.Date(2026, 4, 1, 6, 0, 0, 0, denver)), // too old, ignored
		// 2026-06-15 05:00 UTC = Sunday 23:00 Denver = 06-08 Denver week.
		walk("e", 7000, 0, time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC)),
	}

	got := buildWalking(sessions, now, denver)
	if len(got.WeeklyDistanceSpark) != sparkWeeks {
		t.Errorf("spark length = %d, want %d", len(got.WeeklyDistanceSpark), sparkWeeks)
	}
	want := []float64{0, 0, 0, 0, 0, 5000, 7000, 7000}
	if !reflect.DeepEqual(got.WeeklyDistanceSpark, want) {
		t.Errorf("spark = %v, want %v", got.WeeklyDistanceSpark, want)
	}
}
