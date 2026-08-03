package dashboard

import (
	"math"
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

func ptrI(i int) *int { return &i }

// detailedRun builds a running activity with the optional columns the new
// builder helpers read. hr/gain nil-able via pointers; env "" defaults outdoor.
func detailedRun(name string, dist float64, dur int, start time.Time, pace *float64, hr *int, gain *float64, env string) activity.Activity {
	a := run(name, dist, dur, start)
	a.AvgPaceSecPerKm = pace
	a.AvgHeartRateBpm = hr
	a.ElevationGainMeters = gain
	if env == "" {
		env = "outdoor"
	}
	a.Environment = activity.Environment(env)
	return a
}

// headlineWeek returns the DX's representative week: 4 runs, one indoor (no
// elevation), one with no HR, one 20.4km long run. now is Sat of that week.
func headlineWeek(t *testing.T) ([]activity.Activity, time.Time, *time.Location) {
	t.Helper()
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 8, 1, 18, 0, 0, 0, ny)
	runs := []activity.Activity{
		detailedRun("Easy shakeout", 5633, 2128, time.Date(2026, 7, 27, 7, 0, 0, 0, ny), ptrF(377.8), ptrI(148), ptrF(38), "outdoor"),
		detailedRun("", 4184, 1490, time.Date(2026, 7, 28, 6, 30, 0, 0, ny), ptrF(356.1), ptrI(152), nil, "indoor"),
		detailedRun("Lunch run", 4023, 1575, time.Date(2026, 7, 30, 12, 15, 0, 0, ny), ptrF(391.5), nil, ptrF(22), "outdoor"),
		detailedRun("Saturday long run", 20438, 7784, time.Date(2026, 8, 1, 7, 2, 0, 0, ny), ptrF(380.9), ptrI(156), ptrF(214), "outdoor"),
	}
	return runs, now, ny
}

func TestBuildRunning_CurrentWeekAggregates(t *testing.T) {
	runs, now, ny := headlineWeek(t)
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: len(runs)}}

	got := buildRunning(m, runs, now, ny)
	if got == nil {
		t.Fatal("expected section")
	}
	cw := got.CurrentWeek

	if cw.DurationSeconds != 12977 {
		t.Errorf("duration = %d, want 12977", cw.DurationSeconds)
	}
	// Aggregate pace: 12977 / 34.278 km = 378.58 s/km. The naive mean of the
	// four per-run paces is 376.575 — the long run is what separates them.
	if cw.AvgPaceSecPerKm == nil || math.Abs(*cw.AvgPaceSecPerKm-378.58) > 0.05 {
		t.Errorf("avg pace = %v, want ~378.58 (aggregate, not mean of means)", cw.AvgPaceSecPerKm)
	}
	// Duration-weighted HR over the 3 HR-bearing runs:
	// (2128*148 + 1490*152 + 7784*156) / (2128+1490+7784) = 153.98 → 154.
	if cw.AvgHeartRateBpm == nil || *cw.AvgHeartRateBpm != 154 {
		t.Errorf("avg hr = %v, want 154 (duration-weighted)", cw.AvgHeartRateBpm)
	}
	if cw.HeartRateRuns != 3 {
		t.Errorf("hr runs = %d, want 3", cw.HeartRateRuns)
	}
	if cw.ElevationGainMeters == nil || *cw.ElevationGainMeters != 274 {
		t.Errorf("elevation = %v, want 274 (indoor run contributes nothing)", cw.ElevationGainMeters)
	}
	if cw.ElevationRuns != 3 {
		t.Errorf("elevation runs = %d, want 3", cw.ElevationRuns)
	}
	if cw.LongestRunMeters != 20438 {
		t.Errorf("longest = %v, want 20438", cw.LongestRunMeters)
	}
	if cw.DaysRun != 4 {
		t.Errorf("days run = %d, want 4", cw.DaysRun)
	}
}

func TestBuildRunning_WeekRuns_SortedRowsWithZoneNil(t *testing.T) {
	runs, now, ny := headlineWeek(t)
	// Shuffle input order to prove the builder sorts ascending by StartTime.
	shuffled := []activity.Activity{runs[3], runs[0], runs[2], runs[1]}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: 4}}

	got := buildRunning(m, shuffled, now, ny)
	if len(got.WeekRuns) != 4 {
		t.Fatalf("week runs = %d, want 4", len(got.WeekRuns))
	}
	wantDates := []string{"2026-07-27", "2026-07-28", "2026-07-30", "2026-08-01"}
	for i, wr := range got.WeekRuns {
		if wr.LocalDate != wantDates[i] {
			t.Errorf("week run %d local date = %q, want %q", i, wr.LocalDate, wantDates[i])
		}
		if wr.HeartRateZone != nil {
			t.Errorf("week run %d zone = %v, want nil from the builder (handler fills it)", i, wr.HeartRateZone)
		}
	}
	if got.WeekRuns[1].Environment != "indoor" {
		t.Errorf("run 1 environment = %q, want indoor", got.WeekRuns[1].Environment)
	}
	if got.WeekRuns[1].ElevationGainMeters != nil {
		t.Errorf("indoor run elevation = %v, want nil (not zero)", got.WeekRuns[1].ElevationGainMeters)
	}
	if got.WeekRuns[2].AvgHeartRateBpm != nil {
		t.Errorf("no-HR run bpm = %v, want nil", got.WeekRuns[2].AvgHeartRateBpm)
	}
}

func TestBuildRunning_IndoorOnlyWeek_ElevationNilNotZero(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 8, 1, 18, 0, 0, 0, ny)
	runs := []activity.Activity{
		detailedRun("", 4000, 1400, time.Date(2026, 7, 28, 6, 0, 0, 0, ny), ptrF(350), ptrI(150), nil, "indoor"),
		detailedRun("", 5000, 1800, time.Date(2026, 7, 30, 6, 0, 0, 0, ny), ptrF(360), ptrI(148), nil, "indoor"),
	}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: 2}}

	got := buildRunning(m, runs, now, ny)
	if got.CurrentWeek.ElevationGainMeters != nil {
		t.Errorf("elevation = %v, want nil for an indoor-only week", got.CurrentWeek.ElevationGainMeters)
	}
	if got.CurrentWeek.ElevationRuns != 0 {
		t.Errorf("elevation runs = %d, want 0", got.CurrentWeek.ElevationRuns)
	}
}

func TestBuildRunning_TwoRunsOneDay_DaysRunCountsDistinctDates(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 8, 1, 18, 0, 0, 0, ny)
	runs := []activity.Activity{
		detailedRun("am", 5000, 1800, time.Date(2026, 7, 28, 6, 0, 0, 0, ny), nil, nil, nil, ""),
		detailedRun("pm", 3000, 1100, time.Date(2026, 7, 28, 18, 0, 0, 0, ny), nil, nil, nil, ""),
		detailedRun("", 4000, 1500, time.Date(2026, 7, 30, 6, 0, 0, 0, ny), nil, nil, nil, ""),
	}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: 3}}

	got := buildRunning(m, runs, now, ny)
	if got.CurrentWeek.DaysRun != 2 {
		t.Errorf("days run = %d, want 2 (two runs share a local date)", got.CurrentWeek.DaysRun)
	}
}

func TestBuildRunning_Baseline_WeeksWithRunsDenominator(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 8, 1, 18, 0, 0, 0, ny) // current week Mon 07-27
	runs := []activity.Activity{
		// Window week 07-20: two runs.
		detailedRun("", 5000, 1800, time.Date(2026, 7, 21, 7, 0, 0, 0, ny), ptrF(360), ptrI(150), ptrF(50), ""),
		detailedRun("", 10000, 3600, time.Date(2026, 7, 23, 7, 0, 0, 0, ny), ptrF(360), ptrI(160), nil, ""),
		// Window week 07-06: one run.
		detailedRun("", 6000, 2400, time.Date(2026, 7, 8, 7, 0, 0, 0, ny), ptrF(400), nil, ptrF(30), ""),
		// Weeks 06-29 and 07-13: no runs. Older than the window: excluded.
		detailedRun("", 9000, 3000, time.Date(2026, 6, 24, 7, 0, 0, 0, ny), nil, nil, nil, ""),
		// Current week: excluded from the baseline.
		detailedRun("", 8000, 2900, time.Date(2026, 7, 29, 7, 0, 0, 0, ny), nil, ptrI(170), ptrF(500), ""),
	}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: len(runs)}}

	b := buildRunning(m, runs, now, ny).Baseline
	if b == nil {
		t.Fatal("baseline nil, want present")
	}
	if b.WindowWeeks != 4 || b.Weeks != 2 {
		t.Fatalf("window/weeks = %d/%d, want 4/2 (only weeks holding a run count)", b.WindowWeeks, b.Weeks)
	}
	// 21000m over 2 run-holding weeks.
	if b.DistanceMeters == nil || *b.DistanceMeters != 10500 {
		t.Errorf("distance = %v, want 10500", b.DistanceMeters)
	}
	// 7800s over 2 weeks.
	if b.DurationSeconds == nil || *b.DurationSeconds != 3900 {
		t.Errorf("duration = %v, want 3900", b.DurationSeconds)
	}
	// 3 runs over 2 weeks.
	if b.RunsPerWeek == nil || *b.RunsPerWeek != 1.5 {
		t.Errorf("runs/week = %v, want 1.5", b.RunsPerWeek)
	}
	// Aggregate pace over the window's runs: 7800 / 21 km = 371.43.
	if b.AvgPaceSecPerKm == nil || math.Abs(*b.AvgPaceSecPerKm-371.43) > 0.05 {
		t.Errorf("pace = %v, want ~371.43 (aggregate, not a mean of weekly means)", b.AvgPaceSecPerKm)
	}
	// Duration-weighted HR over the window's HR-bearing runs:
	// (1800*150 + 3600*160) / 5400 = 156.67 → 157.
	if b.AvgHeartRateBpm == nil || *b.AvgHeartRateBpm != 157 {
		t.Errorf("hr = %v, want 157", b.AvgHeartRateBpm)
	}
	// (50 + 30) gain over 2 weeks.
	if b.ElevationGainMeters == nil || *b.ElevationGainMeters != 40 {
		t.Errorf("elevation = %v, want 40", b.ElevationGainMeters)
	}
}

func TestBuildRunning_Baseline_NilForFirstEverRunner(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 8, 1, 18, 0, 0, 0, ny)
	runs := []activity.Activity{
		detailedRun("First run", 3000, 1200, time.Date(2026, 7, 30, 7, 0, 0, 0, ny), nil, nil, nil, ""),
	}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: 1}}

	got := buildRunning(m, runs, now, ny)
	if got.Baseline != nil {
		t.Errorf("baseline = %+v, want nil for a first-ever runner", got.Baseline)
	}
}

func TestBuildRunning_WeeklyLoad_ZeroWeekIsRealZero(t *testing.T) {
	runs, now, ny := headlineWeek(t)
	// One prior-week run so a populated bucket exists beside zero buckets.
	runs = append(runs, detailedRun("", 30900, 11760, time.Date(2026, 7, 22, 7, 0, 0, 0, ny), nil, nil, ptrF(231), ""))
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: len(runs)}}

	load := buildRunning(m, runs, now, ny).WeeklyLoad
	if len(load) != sparkWeeks {
		t.Fatalf("weekly load = %d buckets, want %d", len(load), sparkWeeks)
	}
	// Oldest→newest, ending with the current week.
	if load[len(load)-1].WeekStart != "2026-07-27" {
		t.Errorf("last bucket = %q, want 2026-07-27", load[len(load)-1].WeekStart)
	}
	last := load[len(load)-1]
	if last.RunCount != 4 || last.DurationSeconds != 12977 || last.DistanceMeters != 34278 {
		t.Errorf("current bucket = %+v, want 4 runs / 12977s / 34278m", last)
	}
	prior := load[len(load)-2]
	if prior.WeekStart != "2026-07-20" || prior.RunCount != 1 || prior.ElevationGainMeters == nil || *prior.ElevationGainMeters != 231 {
		t.Errorf("prior bucket = %+v, want 1 run week 2026-07-20 with 231m gain", prior)
	}
	empty := load[len(load)-3]
	if empty.RunCount != 0 || empty.DistanceMeters != 0 || empty.DurationSeconds != 0 {
		t.Errorf("empty bucket = %+v, want a real zero", empty)
	}
	if empty.ElevationGainMeters != nil {
		t.Errorf("empty bucket elevation = %v, want nil", empty.ElevationGainMeters)
	}
}

func TestBuildRunning_DSTWeekBoundary(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	// The US spring-forward week: Sun 2026-03-08 02:00 MST → 03:00 MDT.
	// A run late Sunday local (23:00, after the shift) must land in the week
	// of Mon 2026-03-02, and the current week Mon 2026-03-09 must hold its
	// own run — the AddDate-based bucketing keeps this correct where raw
	// 168-hour arithmetic would drift an hour.
	now := time.Date(2026, 3, 11, 13, 0, 0, 0, denver)
	runs := []activity.Activity{
		detailedRun("dst sunday", 5000, 1800, time.Date(2026, 3, 8, 23, 0, 0, 0, denver), nil, nil, nil, ""),
		detailedRun("this week", 4000, 1500, time.Date(2026, 3, 10, 7, 0, 0, 0, denver), nil, nil, nil, ""),
	}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: 2}}

	got := buildRunning(m, runs, now, denver)
	if len(got.WeekRuns) != 1 || got.WeekRuns[0].LocalDate != "2026-03-10" {
		t.Fatalf("week runs = %+v, want only the 2026-03-10 run", got.WeekRuns)
	}
	load := got.WeeklyLoad
	prior := load[len(load)-2]
	if prior.WeekStart != "2026-03-02" || prior.RunCount != 1 {
		t.Errorf("prior bucket = %+v, want the DST-Sunday run in week 2026-03-02", prior)
	}
}

func TestBuildRunning_Baseline_DurationTruncates(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	now := time.Date(2026, 8, 1, 18, 0, 0, 0, ny) // current week Mon 07-27
	// Two run-holding window weeks summing to 7801s: 7801/2 truncates to 3900.
	runs := []activity.Activity{
		detailedRun("", 5000, 1801, time.Date(2026, 7, 21, 7, 0, 0, 0, ny), nil, nil, nil, ""),
		detailedRun("", 10000, 6000, time.Date(2026, 7, 15, 7, 0, 0, 0, ny), nil, nil, nil, ""),
	}
	m := activity.Metrics{AllTime: activity.PeriodStat{RunCount: len(runs)}}

	b := buildRunning(m, runs, now, ny).Baseline
	if b == nil || b.Weeks != 2 {
		t.Fatalf("baseline = %+v, want 2 run-holding weeks", b)
	}
	if b.DurationSeconds == nil || *b.DurationSeconds != 3900 {
		t.Errorf("duration = %v, want 3900 (7801/2 truncates)", b.DurationSeconds)
	}
}
