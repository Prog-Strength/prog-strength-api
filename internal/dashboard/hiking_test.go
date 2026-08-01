package dashboard

import (
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

func hike(name string, dist float64, dur int, start time.Time, gain *float64) activity.Activity {
	a := activity.Activity{
		ActivityType:        activity.ActivityHiking,
		StartTime:           start,
		DistanceMeters:      dist,
		DurationSeconds:     dur,
		ElevationGainMeters: gain,
	}
	if name != "" {
		a.Name = ptrS(name)
	}
	return a
}

func TestBuildHiking_EmptyReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	if got := buildHiking(nil, now, denver); got != nil {
		t.Errorf("no sessions should be nil, got %+v", got)
	}
}

func TestBuildHiking_OnlyOtherTypesReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	sessions := []activity.Activity{
		run("Run", 5000, 1500, time.Date(2026, 6, 16, 7, 0, 0, 0, denver)),
		walk("Walk", 3000, 1800, time.Date(2026, 6, 16, 8, 0, 0, 0, denver)),
	}
	if got := buildHiking(sessions, now, denver); got != nil {
		t.Errorf("only other types should be nil, got %+v", got)
	}
}

func TestBuildHiking_CurrentWeekPassThrough(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	sessions := []activity.Activity{
		hike("a", 6000, 5400, time.Date(2026, 6, 15, 6, 0, 0, 0, denver), ptrF(200)),
		hike("b", 8000, 7200, time.Date(2026, 6, 17, 6, 0, 0, 0, denver), ptrF(300)),
		hike("c", 12000, 10800, time.Date(2026, 6, 10, 6, 0, 0, 0, denver), ptrF(500)), // last week
	}

	got := buildHiking(sessions, now, denver)
	if got == nil {
		t.Fatal("expected section")
	}
	if got.CurrentWeek.DistanceMeters != 14000 {
		t.Errorf("current week distance = %v, want 14000", got.CurrentWeek.DistanceMeters)
	}
	if got.CurrentWeek.SessionCount != 2 {
		t.Errorf("current week count = %d, want 2", got.CurrentWeek.SessionCount)
	}
	if got.CurrentWeek.DurationSeconds != 12600 {
		t.Errorf("current week duration = %d, want 12600", got.CurrentWeek.DurationSeconds)
	}
}

func TestBuildHiking_LatestIsMaxStartTime(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	sessions := []activity.Activity{
		hike("Morning", 6000, 5400, time.Date(2026, 6, 16, 7, 0, 0, 0, denver), ptrF(200)),
		hike("Newest", 9000, 8000, time.Date(2026, 6, 18, 12, 0, 0, 0, denver), ptrF(400)),
		hike("Old", 4000, 3600, time.Date(2026, 6, 1, 8, 0, 0, 0, denver), nil),
	}

	got := buildHiking(sessions, now, denver)
	if got.LatestSession == nil || got.LatestSession.Name == nil || *got.LatestSession.Name != "Newest" {
		t.Fatalf("latest mismatch: %+v", got.LatestSession)
	}
	if got.LatestSession.DistanceMeters != 9000 || got.LatestSession.DurationSeconds != 8000 {
		t.Errorf("latest fields mismatch: %+v", got.LatestSession)
	}
}

func TestBuildHiking_WeeklySparkZeroFilledAndLocalBucketing(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	// Buckets (Mondays): 04-27,05-04,05-11,05-18,05-25,06-01,06-08,06-15
	sessions := []activity.Activity{
		hike("a", 6000, 0, time.Date(2026, 6, 15, 6, 0, 0, 0, denver), nil),
		hike("b", 8000, 0, time.Date(2026, 6, 17, 6, 0, 0, 0, denver), nil),
		hike("c", 10000, 0, time.Date(2026, 6, 3, 6, 0, 0, 0, denver), nil), // week 06-01
		hike("d", 99999, 0, time.Date(2026, 4, 1, 6, 0, 0, 0, denver), nil), // too old
		// 2026-06-15 05:00 UTC = Sunday 23:00 Denver = 06-08 Denver week.
		hike("e", 5000, 0, time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC), nil),
	}

	got := buildHiking(sessions, now, denver)
	if len(got.WeeklyDistanceSpark) != sparkWeeks {
		t.Errorf("spark length = %d, want %d", len(got.WeeklyDistanceSpark), sparkWeeks)
	}
	want := []float64{0, 0, 0, 0, 0, 10000, 5000, 14000}
	if !reflect.DeepEqual(got.WeeklyDistanceSpark, want) {
		t.Errorf("spark = %v, want %v", got.WeeklyDistanceSpark, want)
	}
}

func TestBuildHiking_ElevationGainSumsCurrentWeekAndSkipsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	sessions := []activity.Activity{
		// Current week, counted.
		hike("with-gain", 6000, 5400, time.Date(2026, 6, 15, 6, 0, 0, 0, denver), ptrF(120)),
		// Current week, nil gain skipped.
		hike("nil-gain", 8000, 7200, time.Date(2026, 6, 17, 6, 0, 0, 0, denver), nil),
		// Last week, excluded even though it has a big gain.
		hike("last-week", 12000, 10800, time.Date(2026, 6, 10, 6, 0, 0, 0, denver), ptrF(999)),
	}

	got := buildHiking(sessions, now, denver)
	if got == nil {
		t.Fatal("expected section")
	}
	if got.ElevationGainMeters != 120 {
		t.Errorf("elevation gain = %v, want 120", got.ElevationGainMeters)
	}
}
