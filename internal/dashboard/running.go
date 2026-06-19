package dashboard

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

// sparkWeeks is the number of weekly buckets the sparklines cover.
const sparkWeeks = 8

// buildRunning assembles the running tile from already-fetched metrics and the
// user's run activities. It is pure: now and loc are passed in (no time.Now,
// no DB) so the local-week bucketing is deterministic and testable across
// timezones and DST. Returns nil when there is no running activity at all.
func buildRunning(metrics activity.Metrics, runs []activity.Activity, now time.Time, loc *time.Location) *RunningSection {
	if len(runs) == 0 && metrics.AllTime.RunCount == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}

	section := &RunningSection{
		CurrentWeek: RunningCurrentWeek{
			DistanceMeters:      metrics.CurrentWeek.DistanceMeters,
			RunCount:            metrics.CurrentWeek.RunCount,
			DeltaPctVsPriorWeek: metrics.DeltaPctVsPriorWeek,
		},
		RecentAvgPaceSecPerKm: metrics.RecentAvgPaceSecPerKm,
		LatestRun:             latestRun(runs),
		WeeklyDistanceSpark:   weeklyDistanceSpark(runs, now, loc),
	}
	return section
}

// latestRun picks the run with the greatest StartTime. Callers usually pass
// runs newest-first, but we don't rely on ordering. Non-running activities are
// ignored. nil when there are no runs.
func latestRun(runs []activity.Activity) *LatestRun {
	var latest *activity.Activity
	for i := range runs {
		if runs[i].ActivityType != activity.ActivityRunning {
			continue
		}
		if latest == nil || runs[i].StartTime.After(latest.StartTime) {
			latest = &runs[i]
		}
	}
	if latest == nil {
		return nil
	}
	return &LatestRun{
		Name:            latest.Name,
		DistanceMeters:  latest.DistanceMeters,
		DurationSeconds: latest.DurationSeconds,
		StartTime:       latest.StartTime,
	}
}

// weeklyDistanceSpark sums running distance into each of the last sparkWeeks
// local weeks, oldest→newest, zero-filling weeks without a run.
func weeklyDistanceSpark(runs []activity.Activity, now time.Time, loc *time.Location) []float64 {
	starts := weeklyBucketStarts(now, loc, sparkWeeks)
	spark := make([]float64, len(starts))
	oldest := starts[0]
	for i := range runs {
		if runs[i].ActivityType != activity.ActivityRunning {
			continue
		}
		ws := localWeekStart(runs[i].StartTime, loc)
		if ws.Before(oldest) {
			continue
		}
		if idx := weekIndex(starts, ws); idx >= 0 {
			spark[idx] += runs[i].DistanceMeters
		}
	}
	return spark
}

// weekIndex finds the bucket whose Monday equals ws. starts is ascending and
// contiguous (one week apart), so an equal-instant match is exact. Returns -1
// when ws falls outside the window (e.g. a future week).
func weekIndex(starts []time.Time, ws time.Time) int {
	for i := range starts {
		if starts[i].Equal(ws) {
			return i
		}
	}
	return -1
}
