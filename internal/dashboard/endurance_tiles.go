package dashboard

import (
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
)

// enduranceRollup is the shared computation behind the walking/cycling/hiking
// builders: current-week distance/count/duration and the 8-week weekly-distance
// spark over the sessions of a single activity type. Pure: now and loc are
// passed in so bucketing is deterministic across timezones and DST.
type enduranceRollup struct {
	current EnduranceCurrentWeek
	spark   []float64
	latest  *EnduranceLatest
	count   int // total sessions of this type in the window (nil-on-empty guard)
}

func computeEnduranceRollup(sessions []activity.Activity, t activity.ActivityType, now time.Time, loc *time.Location) enduranceRollup {
	if loc == nil {
		loc = time.UTC
	}
	starts := weeklyBucketStarts(now, loc, sparkWeeks)
	oldest := starts[0]
	current := localWeekStart(now, loc)
	spark := make([]float64, len(starts))
	roll := enduranceRollup{spark: spark}
	var latest *activity.Activity
	for i := range sessions {
		if sessions[i].ActivityType != t {
			continue
		}
		roll.count++
		ws := localWeekStart(sessions[i].StartTime, loc)
		if ws.Equal(current) {
			roll.current.DistanceMeters += sessions[i].DistanceMeters
			roll.current.SessionCount++
			roll.current.DurationSeconds += sessions[i].DurationSeconds
		}
		if !ws.Before(oldest) {
			if idx := weekIndex(starts, ws); idx >= 0 {
				spark[idx] += sessions[i].DistanceMeters
			}
		}
		if latest == nil || sessions[i].StartTime.After(latest.StartTime) {
			latest = &sessions[i]
		}
	}
	if latest != nil {
		roll.latest = &EnduranceLatest{
			Name:            latest.Name,
			DistanceMeters:  latest.DistanceMeters,
			DurationSeconds: latest.DurationSeconds,
			StartTime:       latest.StartTime,
		}
	}
	return roll
}
