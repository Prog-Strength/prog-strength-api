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

// buildWhoop assembles the Whoop recovery tile over the dashboard's window: the
// last baseline_window_days + 1 local dates ending today.
func buildWhoop(entries []whooprecovery.Entry, eng *recoverytrend.Engine, now time.Time, loc *time.Location) *RecoverySection {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	y, mo, d := local.Date()
	// loc has already done its only job — deciding which date is today — so the
	// arithmetic behind it runs in UTC. In a zone whose DST transition lands AT
	// midnight (America/Havana, America/Santiago) that wall clock does not exist
	// and time.Date normalizes it into the previous day.
	from := time.Date(y, mo, d-eng.BaselineWindowDays(), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	return buildRecoveryView(entries, eng, from, local.Format("2006-01-02"), now, loc)
}

// buildRecoveryView assembles a RecoverySection over an EXPLICIT charted window
// of local dates [from, to], inclusive, oldest→newest. The builder prepends
// eng.BaselineWindowDays() dates of lead-in itself, so every charted day —
// including the first — is measured against a full trailing sample.
//
// It is pure: now and loc are passed in (no time.Now, no DB) so the local-day
// window is deterministic across timezones and DST. The caller gates presence
// on a connected Whoop connection; this builder assumes that gate already
// passed and always returns a non-nil section (a connected user with no data
// yet still gets Today nil, an empty spark, a full null-metric Days window, and
// an unknown baseline/HRV block).
//
// Today and RestingHRSpark are window-independent: they are always the local
// day of now and the 7 days behind it, which is what makes the payload
// identical across every route that serves this section for one user. Days is
// the honest date-aligned history (missing days present with null metrics) with
// each day's own trailing band attached, and Baseline and HRV describe the
// window's FINAL charted day.
//
// BaselineTrend describes that final day too, but is the one block the charted
// width can silence: it measures the baseline against where it stood
// baseline_drift_days ago, so it needs at least baseline_drift_days + 1 CHARTED
// days to have both endpoints. Below that it is "unknown" however much lead-in
// sits behind the window.
func buildRecoveryView(
	entries []whooprecovery.Entry,
	eng *recoverytrend.Engine,
	from, to string, // local YYYY-MM-DD, inclusive
	now time.Time,
	loc *time.Location,
) *RecoverySection {
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

	// Materialize [from − win, to]: every local date present, oldest→newest,
	// missing days carrying null metrics (never omitted, never zero-filled).
	// The first `win` dates are lead-in for the rolling baseline and are NOT
	// serialized.
	win := eng.BaselineWindowDays()
	engineDays := materializeRecoveryDays(from, to, win, byDate)

	// A degenerate window materializes nothing at all; the engine's own nil
	// handling supplies the unknown/zero derived blocks.
	var charted []recoverytrend.Day
	if engineDays != nil {
		charted = engineDays[win:]
	}
	series, drift := eng.ComputeSeries(engineDays)

	// The scalar blocks answer "how does the most recent morning stand against
	// its own trailing baseline", so their sample is one baseline window wide no
	// matter how wide the caller charted. Handing Compute the whole charted
	// window instead would average a wide window's baseline over all of it and
	// starve a narrow one, either way contradicting the final day's own band.
	tail := engineDays
	if len(tail) > win+1 {
		tail = tail[len(tail)-win-1:]
	}
	baseline, hrv := eng.Compute(tail)

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

// materializeRecoveryDays emits every date in [from − lead, to], oldest→newest,
// with a date absent from byDate carried as a slot with null metrics. It returns
// nothing at all for a window it cannot make sense of (an unparseable endpoint,
// or to before from), which is the caller's empty-payload path rather than an
// error worth propagating. Otherwise it always emits at least lead+1 days.
//
// The walk runs in UTC because these are pure calendar dates: the user's zone
// only decides which date is today, and the caller settled that before calling.
// Walking in a zone whose DST transition lands AT midnight would be worse than
// imprecise — that wall clock does not exist, so time.Date normalizes it into
// the previous day and the window silently repeats one date and loses another.
//
// The bound is a string comparison rather than a day count: YYYY-MM-DD sorts
// lexicographically, and a time.Duration saturates at ~292 years, so subtracting
// the endpoints would answer an absurd range with a wrong number rather than an
// error.
func materializeRecoveryDays(from, to string, lead int, byDate map[string]whooprecovery.Entry) []recoverytrend.Day {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil
	}
	if _, err := time.Parse("2006-01-02", to); err != nil {
		return nil
	}
	if to < from {
		return nil
	}

	y, mo, d := start.Date()
	days := make([]recoverytrend.Day, 0, lead+1)
	for i := -lead; ; i++ {
		ds := time.Date(y, mo, d+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		if ds > to {
			return days
		}
		ed := recoverytrend.Day{Date: ds}
		if e, ok := byDate[ds]; ok {
			ed.RestingHR = e.RestingHeartRate
			ed.RecoveryScore = e.RecoveryScore
			ed.HRV = e.HRVRmssdMilli
		}
		days = append(days, ed)
	}
}
