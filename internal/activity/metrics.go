package activity

import "time"

// metricRow is the minimal projection the aggregation needs from a live
// session: when it happened and the two numbers the tiles sum over.
type metricRow struct {
	startTime       time.Time
	distanceMeters  float64
	durationSeconds int
}

// computeMetrics aggregates the dashboard tiles from the user's live
// sessions. Boundaries are computed in loc because a "week" or "month" is
// a user-local calendar concept; a run logged at 11pm local on Sunday
// belongs to that local week, not the UTC one. Aggregating in Go (rather
// than SQL date() math, which only takes a fixed UTC offset and is wrong
// across DST) keeps this correct and shared between both repositories.
func computeMetrics(rows []metricRow, now time.Time, loc *time.Location) Metrics {
	if loc == nil {
		loc = time.UTC
	}
	weekStart, weekEnd := localWeekBounds(now, loc)
	priorStart := weekStart.AddDate(0, 0, -7)
	monthStart, monthEnd := localMonthBounds(now, loc)
	recentStart := now.Add(-30 * 24 * time.Hour)

	var m Metrics
	var priorWeekDist float64
	var recentDist float64
	var recentDur int

	for _, r := range rows {
		t := r.startTime

		if !t.Before(weekStart) && t.Before(weekEnd) {
			m.CurrentWeek.DistanceMeters += r.distanceMeters
			m.CurrentWeek.RunCount++
		}
		if !t.Before(priorStart) && t.Before(weekStart) {
			priorWeekDist += r.distanceMeters
		}
		if !t.Before(monthStart) && t.Before(monthEnd) {
			m.CurrentMonth.DistanceMeters += r.distanceMeters
			m.CurrentMonth.RunCount++
		}
		if !t.Before(recentStart) && !t.After(now) {
			recentDist += r.distanceMeters
			recentDur += r.durationSeconds
		}

		m.AllTime.DistanceMeters += r.distanceMeters
		m.AllTime.RunCount++
	}

	// Delta vs prior week: nil when there's no prior-week baseline.
	if priorWeekDist > 0 {
		delta := (m.CurrentWeek.DistanceMeters - priorWeekDist) / priorWeekDist * 100
		m.DeltaPctVsPriorWeek = &delta
	}

	// Aggregate pace over the window: total time per total distance.
	if recentDist > 0 {
		pace := float64(recentDur) / (recentDist / 1000)
		m.RecentAvgPaceSecPerKm = &pace
	}

	return m
}

// localWeekBounds returns [Monday 00:00 local, next Monday 00:00 local)
// for the ISO week containing now. The end is exclusive.
func localWeekBounds(now time.Time, loc *time.Location) (time.Time, time.Time) {
	local := now.In(loc)
	// Go's Weekday() is Sunday=0..Saturday=6; ISO weeks start Monday.
	offset := (int(local.Weekday()) + 6) % 7
	y, mo, d := local.Date()
	monday := time.Date(y, mo, d-offset, 0, 0, 0, 0, loc)
	return monday, monday.AddDate(0, 0, 7)
}

// localMonthBounds returns [first-of-month 00:00 local, first-of-next-month
// 00:00 local) for the calendar month containing now. The end is exclusive.
func localMonthBounds(now time.Time, loc *time.Location) (time.Time, time.Time) {
	local := now.In(loc)
	y, mo, _ := local.Date()
	start := time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 1, 0)
}
