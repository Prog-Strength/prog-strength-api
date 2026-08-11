// Package whoopsleep stores WHOOP sleep records, one row per WHOOP sleep
// record rather than one per (user, day).
//
// This is the deliberate departure from internal/whooprecovery (and the
// steps-shaped table pattern generally): WHOOP emits a separate record with
// nap=true for daytime sleep, so (user_id, date) is genuinely not unique.
// Keying by WHOOP's own UUID also makes a sleep.deleted webhook a direct
// delete rather than a date-derivation round trip.
//
// Durations are milliseconds exactly as WHOOP sent them — the ingest layer
// does no unit conversion, so a wire value is always recoverable and
// presentation rounding stays the tile's job.
package whoopsleep

import "time"

// Entry is one WHOOP sleep record. Date is the YYYY-MM-DD local WAKE date
// (derived from `end` localized by TimezoneOffset — see the SOW's "Dating a
// Night"). Every score field is a pointer because a PENDING or UNSCORABLE
// record carries a start, an end, and nothing else, and is still stored so the
// row exists when the score arrives.
//
// StartedAt/EndedAt are strings, not time.Time: they are stored as WHOOP's own
// RFC3339 text, and the only consumer needing an order compares them
// lexicographically, which RFC3339 in a fixed zone supports. Parsing and
// reformatting at the storage boundary would silently normalize away WHOOP's
// representation.
type Entry struct {
	ID             string
	UserID         string
	WhoopSleepID   string
	Date           string // YYYY-MM-DD local wake date
	IsNap          bool
	StartedAt      string // RFC3339, as WHOOP sent it
	EndedAt        string // RFC3339, as WHOOP sent it
	TimezoneOffset string // e.g. "-06:00", as WHOOP sent it
	ScoreState     string // SCORED | PENDING | UNSCORABLE

	InBedMilli         *int64
	AwakeMilli         *int64
	NoDataMilli        *int64
	LightSleepMilli    *int64
	SlowWaveSleepMilli *int64
	REMSleepMilli      *int64
	SleepCycleCount    *int64
	DisturbanceCount   *int64

	NeedBaselineMilli      *int64
	NeedFromSleepDebtMilli *int64
	NeedFromStrainMilli    *int64
	// NeedFromNapMilli is legitimately negative — a nap discharges sleep need.
	NeedFromNapMilli *int64

	RespiratoryRate *float64
	PerformancePct  *float64
	ConsistencyPct  *float64
	EfficiencyPct   *float64

	CreatedAt time.Time
	UpdatedAt time.Time
}
