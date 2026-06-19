package dashboard

import "time"

// maxStreakLookback bounds the backward week walk so the loop always
// terminates even for a (hypothetical) unbroken multi-year streak.
const maxStreakLookback = 60

// buildStreak assembles the streak tile from the set of local date strings on
// which the user was active. It is pure: now and loc are passed in so the
// week boundaries (Mon→Sun, local) are deterministic across timezones and
// DST. Always returns a value — an empty streak is a real zero state.
func buildStreak(activeDates map[string]bool, now time.Time, loc *time.Location) StreakSection {
	if loc == nil {
		loc = time.UTC
	}

	currentMonday := localWeekStart(now, loc)

	// Fill the current week's 7 day flags, Mon→Sun.
	var week [7]bool
	active := 0
	for i := 0; i < 7; i++ {
		day := currentMonday.AddDate(0, 0, i)
		if activeDates[day.Format("2006-01-02")] {
			week[i] = true
			active++
		}
	}

	return StreakSection{
		Weeks:              weekStreak(activeDates, currentMonday, loc),
		ActiveDaysThisWeek: active,
		Week:               week,
	}
}

// weekStreak counts consecutive active weeks. A week is active if any of its 7
// local days is in activeDates. The streak is the run of active weeks ending
// at-or-just-before the current week: if the current week is active the run
// includes it; if the current week is inactive (today's streak is "paused")
// the count is the run immediately before it, so a user who trained last week
// but not yet this week still sees their streak.
func weekStreak(activeDates map[string]bool, currentMonday time.Time, loc *time.Location) int {
	streak := 0
	skippedCurrent := false
	for i := 0; i < maxStreakLookback; i++ {
		monday := currentMonday.AddDate(0, 0, -7*i)
		if weekActive(activeDates, monday) {
			streak++
			continue
		}
		// Inactive week. If it's the current week, the streak hasn't broken
		// yet — keep looking at prior weeks for the existing run. Otherwise
		// the run has ended.
		if i == 0 && !skippedCurrent {
			skippedCurrent = true
			continue
		}
		break
	}
	return streak
}

// weekActive reports whether any of the 7 local days starting at monday is in
// activeDates.
func weekActive(activeDates map[string]bool, monday time.Time) bool {
	for i := 0; i < 7; i++ {
		day := monday.AddDate(0, 0, i)
		if activeDates[day.Format("2006-01-02")] {
			return true
		}
	}
	return false
}
