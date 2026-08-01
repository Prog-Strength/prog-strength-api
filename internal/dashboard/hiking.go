package dashboard

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

// buildHiking assembles the hiking tile: like walking/cycling plus this week's
// summed elevation gain from Activity.ElevationGainMeters (nil gains skipped).
func buildHiking(sessions []activity.Activity, now time.Time, loc *time.Location) *HikingSection {
	roll := computeEnduranceRollup(sessions, activity.ActivityHiking, now, loc)
	if roll.count == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}
	current := localWeekStart(now, loc)
	var gain float64
	for i := range sessions {
		if sessions[i].ActivityType != activity.ActivityHiking {
			continue
		}
		if !localWeekStart(sessions[i].StartTime, loc).Equal(current) {
			continue
		}
		if sessions[i].ElevationGainMeters != nil {
			gain += *sessions[i].ElevationGainMeters
		}
	}
	return &HikingSection{
		CurrentWeek:         roll.current,
		ElevationGainMeters: gain,
		LatestSession:       roll.latest,
		WeeklyDistanceSpark: roll.spark,
	}
}
