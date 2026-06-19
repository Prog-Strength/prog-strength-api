package dashboard

import (
	"math"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
)

// sparkDays is the number of daily buckets the steps sparkline covers.
const sparkDays = 7

// buildSteps assembles the steps tile from already-fetched daily entries and
// the user's goal. It is pure: now and loc are passed in (no time.Now, no DB)
// so the local-day window is deterministic across timezones and DST. Returns
// nil when there are no entries at all.
func buildSteps(entries []steps.Entry, goal steps.Goal, now time.Time, loc *time.Location) *StepsSection {
	if len(entries) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}

	// Index entries by their YYYY-MM-DD date so the window lookup is O(1).
	byDate := make(map[string]int, len(entries))
	for _, e := range entries {
		byDate[e.Date] = e.Steps
	}

	// Build the last sparkDays local calendar dates oldest→newest.
	local := now.In(loc)
	y, mo, d := local.Date()
	todayStr := local.Format("2006-01-02")
	spark := make([]int, sparkDays)
	var sum int
	for i := 0; i < sparkDays; i++ {
		// AddDate keeps DST correctness vs. subtracting raw hours.
		day := time.Date(y, mo, d-(sparkDays-1-i), 0, 0, 0, 0, loc)
		v := byDate[day.Format("2006-01-02")]
		spark[i] = v
		sum += v
	}

	avg := int(math.Round(float64(sum) / float64(sparkDays)))

	return &StepsSection{
		Avg:        avg,
		Today:      byDate[todayStr],
		Goal:       stepsGoal(goal),
		DailySpark: spark,
	}
}

// stepsGoal returns a pointer to the goal value, or nil when unset. The read
// path represents "never set" as the zero value with a nil CreatedAt, so a
// real goal must have both a timestamp and a positive count.
func stepsGoal(goal steps.Goal) *int {
	if goal.CreatedAt == nil || goal.Goal == 0 {
		return nil
	}
	v := goal.Goal
	return &v
}
