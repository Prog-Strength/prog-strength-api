// Package whooprecovery stores Whoop daily recovery metrics keyed by user +
// local calendar date.
//
// The table is steps-shaped (cf. internal/steps): one upserted row per
// (user, date), latest-wins, hard-deleted. Unlike steps the metrics are
// nullable REALs — Whoop may not have computed a recovery score, resting
// heart rate, or HRV for a given day — so nil pointers map to SQL NULL.
// sleep_id is retained because Whoop v2 webhook delete events identify a
// recovery by its associated sleep UUID rather than by date.
package whooprecovery

import "time"

// Entry is one day's Whoop recovery snapshot. Date is the YYYY-MM-DD local
// calendar day the metric belongs to; the (UserID, Date) pair is unique, so
// the storage layer upserts rather than inserts. The three metric fields are
// nullable: a nil pointer means Whoop had no value and is stored as SQL NULL.
type Entry struct {
	ID               string
	UserID           string
	Date             string   // YYYY-MM-DD local calendar date
	RecoveryScore    *float64 // nullable (Whoop 0-100)
	RestingHeartRate *float64 // nullable, bpm
	HRVRmssdMilli    *float64 // nullable, ms
	CycleID          int64
	SleepID          string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
