package dashboard

import (
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

func ptrF(f float64) *float64 { return &f }
func ptrS(s string) *string   { return &s }

func run(name string, dist float64, dur int, start time.Time) activity.Activity {
	a := activity.Activity{
		ActivityType:    activity.ActivityRunning,
		StartTime:       start,
		DistanceMeters:  dist,
		DurationSeconds: dur,
	}
	if name != "" {
		a.Name = ptrS(name)
	}
	return a
}

func TestBuildRunning_EmptyReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	if got := buildRunning(activity.Metrics{}, nil, now, denver); got != nil {
		t.Errorf("no runs and zero all-time count should be nil, got %+v", got)
	}
	// Metrics with all-time count but no runs slice still yields a section.
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: 5}}
	if got := buildRunning(m, nil, now, denver); got == nil {
		t.Error("all-time count > 0 should yield a section even with empty runs slice")
	}
}

func TestBuildRunning_PassThroughAndLatest(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver) // Wednesday

	m := activity.Metrics{
		CurrentWeek:           activity.PeriodStat{DistanceMeters: 21214.5, RunCount: 3},
		DeltaPctVsPriorWeek:   ptrF(9.0),
		RecentAvgPaceSecPerKm: ptrF(376.5),
		AllTime:               activity.PeriodStat{RunCount: 10},
	}
	// Intentionally not newest-first to prove max-StartTime selection.
	runs := []activity.Activity{
		run("Morning Run", 5000, 1500, time.Date(2026, 6, 16, 7, 0, 0, 0, denver)),
		run("Lunch Run", 8449, 3184, time.Date(2026, 6, 18, 12, 2, 0, 0, denver)),
		run("Old Run", 3000, 900, time.Date(2026, 6, 1, 8, 0, 0, 0, denver)),
	}

	got := buildRunning(m, runs, now, denver)
	if got == nil {
		t.Fatal("expected section")
		return
	}
	if got.CurrentWeek.DistanceMeters != 21214.5 || got.CurrentWeek.RunCount != 3 {
		t.Errorf("current week mismatch: %+v", got.CurrentWeek)
	}
	if got.CurrentWeek.DeltaPctVsPriorWeek == nil || *got.CurrentWeek.DeltaPctVsPriorWeek != 9.0 {
		t.Errorf("delta mismatch: %v", got.CurrentWeek.DeltaPctVsPriorWeek)
	}
	if got.RecentAvgPaceSecPerKm == nil || *got.RecentAvgPaceSecPerKm != 376.5 {
		t.Errorf("pace mismatch: %v", got.RecentAvgPaceSecPerKm)
	}
	if got.LatestRun == nil || got.LatestRun.Name == nil || *got.LatestRun.Name != "Lunch Run" {
		t.Fatalf("latest run mismatch: %+v", got.LatestRun)
	}
	if got.LatestRun.DistanceMeters != 8449 || got.LatestRun.DurationSeconds != 3184 {
		t.Errorf("latest run fields mismatch: %+v", got.LatestRun)
	}
}

func TestBuildRunning_LatestIgnoresNonRunning(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	walk := activity.Activity{
		ActivityType: activity.ActivityWalking,
		StartTime:    time.Date(2026, 6, 19, 9, 0, 0, 0, denver), // newest overall
	}
	r := run("Real Run", 4000, 1200, time.Date(2026, 6, 16, 7, 0, 0, 0, denver))
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: 1}}

	got := buildRunning(m, []activity.Activity{walk, r}, now, denver)
	if got.LatestRun == nil || *got.LatestRun.Name != "Real Run" {
		t.Errorf("should pick newest running activity, got %+v", got.LatestRun)
	}
}

func TestBuildRunning_WeeklySparkZeroFilledAndLocalBucketing(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver) // Wed; current week Mon = 06-15

	// Buckets (Mondays): 04-27,05-04,05-11,05-18,05-25,06-01,06-08,06-15
	runs := []activity.Activity{
		// Current week (06-15): two runs sum.
		run("a", 10000, 0, time.Date(2026, 6, 15, 6, 0, 0, 0, denver)),
		run("b", 11214.5, 0, time.Date(2026, 6, 17, 6, 0, 0, 0, denver)),
		// Week 06-01.
		run("c", 5000, 0, time.Date(2026, 6, 3, 6, 0, 0, 0, denver)),
		// Out of window (too old): ignored.
		run("d", 9999, 0, time.Date(2026, 4, 1, 6, 0, 0, 0, denver)),
		// A run at 2026-06-15 05:00 UTC = Sunday 23:00 Denver, which is the
		// 06-08 Denver week, NOT 06-15. Proves local (not UTC) bucketing.
		run("e", 7000, 0, time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC)),
	}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: len(runs)}}

	got := buildRunning(m, runs, now, denver)
	want := []float64{0, 0, 0, 0, 0, 5000, 7000, 21214.5}
	if !reflect.DeepEqual(got.WeeklyDistanceSpark, want) {
		t.Errorf("spark = %v, want %v", got.WeeklyDistanceSpark, want)
	}
	if len(got.WeeklyDistanceSpark) != sparkWeeks {
		t.Errorf("spark length = %d, want %d", len(got.WeeklyDistanceSpark), sparkWeeks)
	}
}
