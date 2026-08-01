package dashboard

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

// buildWalking assembles the walking tile from the already-fetched endurance
// slice. Pure; nil when the user has no walking session at all.
func buildWalking(sessions []activity.Activity, now time.Time, loc *time.Location) *WalkingSection {
	roll := computeEnduranceRollup(sessions, activity.ActivityWalking, now, loc)
	if roll.count == 0 {
		return nil
	}
	return &WalkingSection{
		CurrentWeek:         roll.current,
		LatestSession:       roll.latest,
		WeeklyDistanceSpark: roll.spark,
	}
}
