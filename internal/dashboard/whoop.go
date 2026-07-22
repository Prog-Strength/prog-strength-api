package dashboard

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

// recoverySparkDays is the number of trailing local days the recovery tile's
// resting-HR sparkline covers. Mirrors sparkDays for steps.
const recoverySparkDays = 7

// buildWhoop assembles the Whoop recovery tile from already-fetched daily
// recovery entries. It is pure: now and loc are passed in (no time.Now, no DB)
// so the local-day window is deterministic across timezones and DST. The caller
// gates presence on a connected Whoop connection; this builder assumes that gate
// already passed and always returns a non-nil section (a connected user with no
// data yet still shows the card with Today nil and an empty spark).
func buildWhoop(entries []whooprecovery.Entry, now time.Time, loc *time.Location) *RecoverySection {
	if loc == nil {
		loc = time.UTC
	}

	// Index entries by their YYYY-MM-DD date for O(1) window lookups.
	byDate := make(map[string]whooprecovery.Entry, len(entries))
	for _, e := range entries {
		byDate[e.Date] = e
	}

	local := now.In(loc)
	y, mo, d := local.Date()
	todayStr := local.Format("2006-01-02")

	section := &RecoverySection{RestingHRSpark: []float64{}}

	// Today's row, if present.
	if e, ok := byDate[todayStr]; ok {
		section.Today = &RecoveryDay{
			Date:             e.Date,
			RestingHeartRate: e.RestingHeartRate,
			RecoveryScore:    e.RecoveryScore,
			HRVRmssdMilli:    e.HRVRmssdMilli,
		}
	}

	// Trailing 7 local days oldest→newest; include a day only when it has a row
	// with a non-null resting heart rate (missing days are omitted, not zeroed).
	for i := recoverySparkDays - 1; i >= 0; i-- {
		day := time.Date(y, mo, d-i, 0, 0, 0, 0, loc)
		if e, ok := byDate[day.Format("2006-01-02")]; ok && e.RestingHeartRate != nil {
			section.RestingHRSpark = append(section.RestingHRSpark, *e.RestingHeartRate)
		}
	}

	return section
}
