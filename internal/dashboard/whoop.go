package dashboard

import (
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/recoverytrend"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
)

// recoverySparkDays is the number of trailing local days the recovery tile's
// resting-HR sparkline covers. Mirrors sparkDays for steps. Deliberately kept
// at 7 with its gap-omitting semantics — the shipped RecoveryCard depends on
// it; the date-aligned Days slice is the correct replacement for anything new.
const recoverySparkDays = 7

// buildWhoop assembles the Whoop recovery tile from already-fetched daily
// recovery entries. It is pure: now and loc are passed in (no time.Now, no DB)
// so the local-day window is deterministic across timezones and DST. The caller
// gates presence on a connected Whoop connection; this builder assumes that gate
// already passed and always returns a non-nil section (a connected user with no
// data yet still shows the card with Today nil, an empty spark, a full
// null-metric Days window, and an unknown baseline/HRV block).
//
// Today and RestingHRSpark are built exactly as before; Days is the honest
// date-aligned history (missing days present with null metrics), and the engine
// derives Baseline and HRV from it.
func buildWhoop(entries []whooprecovery.Entry, eng *recoverytrend.Engine, now time.Time, loc *time.Location) *RecoverySection {
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
	// Unchanged legacy semantics — the shipped card reads this.
	for i := recoverySparkDays - 1; i >= 0; i-- {
		day := time.Date(y, mo, d-i, 0, 0, 0, 0, loc)
		if e, ok := byDate[day.Format("2006-01-02")]; ok && e.RestingHeartRate != nil {
			section.RestingHRSpark = append(section.RestingHRSpark, *e.RestingHeartRate)
		}
	}

	// The full date-aligned window: baseline_window_days local dates preceding
	// today, plus today — oldest→newest, every day present, missing days carrying
	// null metrics (never omitted, never zero-filled).
	win := eng.BaselineWindowDays()
	section.Days = make([]RecoveryDay, 0, win+1)
	for i := win; i >= 0; i-- {
		day := time.Date(y, mo, d-i, 0, 0, 0, 0, loc)
		ds := day.Format("2006-01-02")
		rd := RecoveryDay{Date: ds}
		if e, ok := byDate[ds]; ok {
			rd.RestingHeartRate = e.RestingHeartRate
			rd.RecoveryScore = e.RecoveryScore
			rd.HRVRmssdMilli = e.HRVRmssdMilli
		}
		section.Days = append(section.Days, rd)
	}

	// Derive the baseline and HRV blocks from the date-aligned window.
	engineDays := make([]recoverytrend.Day, len(section.Days))
	for i, rd := range section.Days {
		engineDays[i] = recoverytrend.Day{
			Date:          rd.Date,
			RestingHR:     rd.RestingHeartRate,
			HRV:           rd.HRVRmssdMilli,
			RecoveryScore: rd.RecoveryScore,
		}
	}
	baseline, hrv := eng.Compute(engineDays)

	section.Baseline = RecoveryBaseline{
		WindowDays:        baseline.WindowDays,
		RestingHRAvg:      baseline.RestingHRAvg,
		RestingHRDays:     baseline.RestingHRDays,
		HRVAvg:            baseline.HRVAvg,
		HRVStdDev:         baseline.HRVStdDev,
		HRVDays:           baseline.HRVDays,
		RecoveryScoreAvg:  baseline.RecoveryScoreAvg,
		RecoveryScoreDays: baseline.RecoveryScoreDays,
	}
	section.HRV = RecoveryHRV{
		Status:       hrv.Status,
		BalancedLow:  hrv.BalancedLow,
		BalancedHigh: hrv.BalancedHigh,
		ZScore:       hrv.ZScore,
		Trend:        hrv.Trend,
		ShortAvg:     hrv.ShortAvg,
	}

	return section
}
