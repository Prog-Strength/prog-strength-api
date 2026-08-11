package dashboard

import (
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/whoopsleep"
)

// sleepWindowDays is how many trailing local dates the sleep tile charts.
// Matched to the SOW's rollout promise that reconnecting backfills the trailing
// ~30 nights, so the tile is non-empty the moment a user consents rather than
// filling in a night at a time.
const sleepWindowDays = 30

// buildSleep assembles the sleep tile from already-fetched sleep records. It is
// pure: now and loc are passed in (no time.Now, no DB) so the local-day window
// is deterministic across timezones and DST. The caller gates presence on a
// connected Whoop connection; this builder assumes that gate already passed and
// always returns a non-nil section — a connected user with no data yet still
// shows the card with LastNight nil and a full null-metric window.
func buildSleep(entries []whoopsleep.Entry, now time.Time, loc *time.Location) *SleepSection {
	if loc == nil {
		loc = time.UTC
	}

	// Several records can share a local date (a nap, or a night WHOOP split),
	// so this indexes to a slice and pickNight resolves the group.
	byDate := make(map[string][]whoopsleep.Entry, len(entries))
	for _, e := range entries {
		byDate[e.Date] = append(byDate[e.Date], e)
	}

	local := now.In(loc)
	y, mo, d := local.Date()

	section := &SleepSection{Nights: make([]SleepNight, 0, sleepWindowDays)}
	for i := sleepWindowDays - 1; i >= 0; i-- {
		date := time.Date(y, mo, d-i, 0, 0, 0, 0, loc).Format("2006-01-02")
		section.Nights = append(section.Nights, sleepNight(date, pickNight(byDate[date])))
	}

	// Today's slot doubles as LastNight, but only when a real record backs it:
	// the last element of Nights is null-metric when today has nothing (or only
	// a nap), and a null-metric LastNight would read as "you slept, unmeasured".
	todayStr := local.Format("2006-01-02")
	if e := pickNight(byDate[todayStr]); e != nil {
		n := sleepNight(todayStr, e)
		section.LastNight = &n
	}
	return section
}

// pickNight returns THE night for a local date: the non-nap record, or — when
// more than one non-nap record shares a date (a fragmented night WHOOP split,
// or travel across a date line) — the one with the longest in-bed duration,
// tie-broken by latest ended_at.
//
// It lives in one function with its own test rather than open-coded at call
// sites precisely because the tie-break is the part a second implementation
// would get subtly different.
func pickNight(records []whoopsleep.Entry) *whoopsleep.Entry {
	var best *whoopsleep.Entry
	for i := range records {
		if records[i].IsNap {
			continue
		}
		if best == nil || beatsNight(records[i], *best) {
			best = &records[i]
		}
	}
	return best
}

// beatsNight reports whether candidate outranks best for the same date. A record
// WHOOP has not scored has no in-bed duration AT ALL (not a zero one), so it
// loses to any record that has one and two of them fall through to the ended_at
// tie-break rather than comparing equal-at-zero. EndedAt is compared as text:
// records sharing a local date share an offset, and RFC3339 in a fixed zone
// orders lexicographically.
func beatsNight(candidate, best whoopsleep.Entry) bool {
	switch {
	case candidate.InBedMilli != nil && best.InBedMilli == nil:
		return true
	case candidate.InBedMilli == nil && best.InBedMilli != nil:
		return false
	case candidate.InBedMilli != nil && *candidate.InBedMilli != *best.InBedMilli:
		return *candidate.InBedMilli > *best.InBedMilli
	default:
		return candidate.EndedAt > best.EndedAt
	}
}

// sleepNight projects a record onto the wire shape for the given date. A nil
// record yields the date with every metric null — the honest representation of
// a night we have no data for.
func sleepNight(date string, e *whoopsleep.Entry) SleepNight {
	n := SleepNight{Date: date}
	if e == nil {
		return n
	}
	n.InBedMilli = e.InBedMilli
	n.AwakeMilli = e.AwakeMilli
	n.LightSleepMilli = e.LightSleepMilli
	n.SlowWaveSleepMilli = e.SlowWaveSleepMilli
	n.RemSleepMilli = e.REMSleepMilli
	n.NoDataMilli = e.NoDataMilli
	n.SleepCycleCount = e.SleepCycleCount
	n.DisturbanceCount = e.DisturbanceCount
	n.NeedBaselineMilli = e.NeedBaselineMilli
	n.NeedFromSleepDebtMilli = e.NeedFromSleepDebtMilli
	n.NeedFromStrainMilli = e.NeedFromStrainMilli
	n.NeedFromNapMilli = e.NeedFromNapMilli
	n.RespiratoryRate = e.RespiratoryRate
	n.PerformancePct = e.PerformancePct
	n.ConsistencyPct = e.ConsistencyPct
	n.EfficiencyPct = e.EfficiencyPct
	return n
}
