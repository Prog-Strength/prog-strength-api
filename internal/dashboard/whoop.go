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
// date-aligned history (missing days present with null metrics) with each day's
// own trailing band attached, and the engine derives Baseline, HRV, and
// BaselineTrend from a window that carries baseline_window_days of lead-in
// ahead of the charted days.
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

	// Materialize the WIDE window: 2*win + 1 local dates ending today,
	// oldest→newest, every date present, missing days carrying null metrics
	// (never omitted, never zero-filled). The first `win` dates are lead-in for
	// the rolling baseline and are NOT serialized.
	win := eng.BaselineWindowDays()
	total := 2*win + 1
	engineDays := make([]recoverytrend.Day, 0, total)
	for i := total - 1; i >= 0; i-- {
		day := time.Date(y, mo, d-i, 0, 0, 0, 0, loc)
		ds := day.Format("2006-01-02")
		ed := recoverytrend.Day{Date: ds}
		if e, ok := byDate[ds]; ok {
			ed.RestingHR = e.RestingHeartRate
			ed.RecoveryScore = e.RecoveryScore
			ed.HRV = e.HRVRmssdMilli
		}
		engineDays = append(engineDays, ed)
	}

	// The charted window is the last win+1 dates — byte-for-byte the window
	// this function built before the lead-in was added.
	charted := engineDays[win:]
	series, drift := eng.ComputeSeries(engineDays)
	baseline, hrv := eng.Compute(charted)

	// Zip the charted metrics with their per-day bands. Index-aligned by
	// construction: ComputeSeries returns exactly one result per charted day,
	// in the same order.
	section.Days = make([]RecoveryDayPoint, len(charted))
	for i, ed := range charted {
		section.Days[i] = RecoveryDayPoint{
			RecoveryDay: RecoveryDay{
				Date:             ed.Date,
				RestingHeartRate: ed.RestingHR,
				RecoveryScore:    ed.RecoveryScore,
				HRVRmssdMilli:    ed.HRV,
			},
			BaselineAvg:  series[i].BaselineAvg,
			BalancedLow:  series[i].BalancedLow,
			BalancedHigh: series[i].BalancedHigh,
			ZScore:       series[i].ZScore,
			Status:       series[i].Status,
		}
	}

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
	section.BaselineTrend = RecoveryBaselineTrend{
		Direction: drift.Direction,
		DeltaMs:   drift.DeltaMs,
		FromAvg:   drift.FromAvg,
		OverDays:  drift.OverDays,
	}

	return section
}
