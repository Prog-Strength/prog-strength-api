package dashboard

import (
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
)

// buildCycling assembles the cycling tile from the already-fetched endurance
// slice. Pure; nil when the user has no cycling session at all.
func buildCycling(sessions []activity.Activity, now time.Time, loc *time.Location) *CyclingSection {
	roll := computeEnduranceRollup(sessions, activity.ActivityCycling, now, loc)
	if roll.count == 0 {
		return nil
	}
	return &CyclingSection{
		CurrentWeek:         roll.current,
		LatestSession:       roll.latest,
		WeeklyDistanceSpark: roll.spark,
	}
}
