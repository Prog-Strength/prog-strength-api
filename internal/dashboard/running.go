package dashboard

import (
	"math"
	"sort"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
)

// sparkWeeks is the number of weekly buckets the sparklines cover.
const sparkWeeks = 8

// baselineWindowWeeks is the trailing window behind RunningBaseline.
const baselineWindowWeeks = 4

// buildRunning assembles the running section from already-fetched metrics and
// the user's run activities. It is pure: now and loc are passed in (no
// time.Now, no DB) so the local-week bucketing is deterministic and testable
// across timezones and DST. Returns nil when there is no running activity at
// all. CurrentWeek's DistanceMeters/RunCount stay metrics-sourced (the two
// figures already on screen); every new field derives from the run slice.
// HeartRateZone is left nil here — the handler classifies when Run Effort is
// enabled.
func buildRunning(metrics activity.Metrics, runs []activity.Activity, now time.Time, loc *time.Location) *RunningSection {
	if len(runs) == 0 && metrics.AllTime.RunCount == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}

	week := currentWeekRuns(runs, now, loc)
	return &RunningSection{
		CurrentWeek:           currentWeekStats(metrics, week, loc),
		Baseline:              buildBaseline(runs, now, loc),
		RecentAvgPaceSecPerKm: metrics.RecentAvgPaceSecPerKm,
		LatestRun:             latestRun(runs),
		WeekRuns:              weekRunRows(week, loc),
		WeeklyLoad:            weeklyLoad(runs, now, loc),
		WeeklyDistanceSpark:   weeklyDistanceSpark(runs, now, loc),
	}
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

// currentWeekRuns filters to the running rows of the current local week,
// sorted ascending by start time (the order the week-log tile renders).
func currentWeekRuns(runs []activity.Activity, now time.Time, loc *time.Location) []activity.Activity {
	current := localWeekStart(now, loc)
	var week []activity.Activity
	for i := range runs {
		if runs[i].ActivityType != activity.ActivityRunning {
			continue
		}
		if localWeekStart(runs[i].StartTime, loc).Equal(current) {
			week = append(week, runs[i])
		}
	}
	sort.Slice(week, func(i, j int) bool { return week[i].StartTime.Before(week[j].StartTime) })
	return week
}

// currentWeekStats aggregates the current week. DistanceMeters and RunCount
// come from metrics, unchanged — they are the two figures already on screen
// and the ones the deep page prints; leaving their provenance alone means
// this section cannot silently move a number a user is looking at (the
// handler test pins that the two code paths agree on a shared fixture).
func currentWeekStats(metrics activity.Metrics, week []activity.Activity, loc *time.Location) RunningCurrentWeek {
	cw := RunningCurrentWeek{
		DistanceMeters:      metrics.CurrentWeek.DistanceMeters,
		RunCount:            metrics.CurrentWeek.RunCount,
		DeltaPctVsPriorWeek: metrics.DeltaPctVsPriorWeek,
	}
	var dist float64
	days := make(map[string]bool, len(week))
	for i := range week {
		r := &week[i]
		cw.DurationSeconds += r.DurationSeconds
		dist += r.DistanceMeters
		if r.DistanceMeters > cw.LongestRunMeters {
			cw.LongestRunMeters = r.DistanceMeters
		}
		days[r.StartTime.In(loc).Format("2006-01-02")] = true
	}
	cw.DaysRun = len(days)
	cw.AvgPaceSecPerKm = aggregatePace(cw.DurationSeconds, dist)
	cw.AvgHeartRateBpm = durationWeightedHR(week)
	cw.HeartRateRuns = countHeartRateRuns(week)
	cw.ElevationGainMeters, cw.ElevationRuns = sumElevation(week)
	return cw
}

// aggregatePace is Σduration / Σkm — the same method the 30-day figure uses,
// so week and baseline paces are comparable. nil when there is no distance.
func aggregatePace(durationSeconds int, distanceMeters float64) *float64 {
	if distanceMeters <= 0 {
		return nil
	}
	p := float64(durationSeconds) / (distanceMeters / 1000)
	return &p
}

// durationWeightedHR weights each HR-bearing run's average by its duration —
// a 65-minute long run and a 20-minute shakeout are not equal evidence about
// the week's effort. nil when no run carries both HR and a positive duration.
func durationWeightedHR(runs []activity.Activity) *int {
	var num, den float64
	for i := range runs {
		if runs[i].AvgHeartRateBpm == nil || runs[i].DurationSeconds <= 0 {
			continue
		}
		num += float64(runs[i].DurationSeconds) * float64(*runs[i].AvgHeartRateBpm)
		den += float64(runs[i].DurationSeconds)
	}
	if den == 0 {
		return nil
	}
	v := int(math.Round(num / den))
	return &v
}

func countHeartRateRuns(runs []activity.Activity) int {
	n := 0
	for i := range runs {
		if runs[i].AvgHeartRateBpm != nil {
			n++
		}
	}
	return n
}

// sumElevation sums gain over gain-bearing runs. nil — never 0 — when none
// carry gain: a treadmill week and a flat week are different facts.
func sumElevation(runs []activity.Activity) (*float64, int) {
	var sum float64
	n := 0
	for i := range runs {
		if runs[i].ElevationGainMeters == nil {
			continue
		}
		sum += *runs[i].ElevationGainMeters
		n++
	}
	if n == 0 {
		return nil, 0
	}
	return &sum, n
}

// weekRunRows projects the (already sorted) current-week runs onto the wire
// shape. HeartRateZone stays nil; the handler fills it when Run Effort is on.
func weekRunRows(week []activity.Activity, loc *time.Location) []RunningWeekRun {
	rows := make([]RunningWeekRun, 0, len(week))
	for i := range week {
		r := &week[i]
		rows = append(rows, RunningWeekRun{
			ActivityID:          r.ID,
			Name:                r.Name,
			StartTime:           r.StartTime,
			LocalDate:           r.StartTime.In(loc).Format("2006-01-02"),
			DistanceMeters:      r.DistanceMeters,
			DurationSeconds:     r.DurationSeconds,
			AvgPaceSecPerKm:     r.AvgPaceSecPerKm,
			AvgHeartRateBpm:     r.AvgHeartRateBpm,
			ElevationGainMeters: r.ElevationGainMeters,
			Environment:         string(r.Environment),
		})
	}
	return rows
}

// buildBaseline averages the four buckets BEFORE the current week over the
// weeks that actually held a run (see RunningBaseline). nil when none did.
func buildBaseline(runs []activity.Activity, now time.Time, loc *time.Location) *RunningBaseline {
	starts := weeklyBucketStarts(now, loc, baselineWindowWeeks+1)[:baselineWindowWeeks]
	var window []activity.Activity
	weekHasRun := make([]bool, len(starts))
	for i := range runs {
		if runs[i].ActivityType != activity.ActivityRunning {
			continue
		}
		idx := weekIndex(starts, localWeekStart(runs[i].StartTime, loc))
		if idx < 0 {
			continue
		}
		window = append(window, runs[i])
		weekHasRun[idx] = true
	}
	weeks := 0
	for _, has := range weekHasRun {
		if has {
			weeks++
		}
	}
	if weeks == 0 {
		return nil
	}

	var dist float64
	var dur int
	for i := range window {
		dist += window[i].DistanceMeters
		dur += window[i].DurationSeconds
	}
	b := &RunningBaseline{WindowWeeks: baselineWindowWeeks, Weeks: weeks}
	avgDist := dist / float64(weeks)
	b.DistanceMeters = &avgDist
	// Integer division, truncating: sub-second precision is meaningless for
	// a weekly duration average the tiles render as h:mm.
	avgDur := dur / weeks
	b.DurationSeconds = &avgDur
	rpw := float64(len(window)) / float64(weeks)
	b.RunsPerWeek = &rpw
	b.AvgPaceSecPerKm = aggregatePace(dur, dist)
	b.AvgHeartRateBpm = durationWeightedHR(window)
	if gain, n := sumElevation(window); n > 0 {
		avgGain := *gain / float64(weeks)
		b.ElevationGainMeters = &avgGain
	}
	return b
}

// weeklyLoad buckets runs into the last sparkWeeks local weeks,
// oldest→newest. A bucket with no runs is a real zero (count 0, sums 0, nil
// elevation) — the distinction weekly_distance_spark could not make.
func weeklyLoad(runs []activity.Activity, now time.Time, loc *time.Location) []RunningWeekPoint {
	starts := weeklyBucketStarts(now, loc, sparkWeeks)
	points := make([]RunningWeekPoint, len(starts))
	for i := range starts {
		points[i] = RunningWeekPoint{WeekStart: starts[i].Format("2006-01-02")}
	}
	for i := range runs {
		if runs[i].ActivityType != activity.ActivityRunning {
			continue
		}
		idx := weekIndex(starts, localWeekStart(runs[i].StartTime, loc))
		if idx < 0 {
			continue
		}
		p := &points[idx]
		p.DistanceMeters += runs[i].DistanceMeters
		p.DurationSeconds += runs[i].DurationSeconds
		p.RunCount++
		if g := runs[i].ElevationGainMeters; g != nil {
			if p.ElevationGainMeters == nil {
				var zero float64
				p.ElevationGainMeters = &zero
			}
			*p.ElevationGainMeters += *g
		}
	}
	return points
}
