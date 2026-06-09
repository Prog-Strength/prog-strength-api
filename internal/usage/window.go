// Package usage computes a user's daily external-API spend and exposes
// it as a percentage of an env-configured dollar cap via GET /me/usage.
// Cost is derived at read time from telemetry rows (agent_turns for the
// LLM half, agent_speak_calls for the TTS half) priced through a
// server-side price table. No dollar figure ever crosses the wire — the
// response DTO carries only {percent_used, capped, resets_at}.
package usage

import "time"

// LocalDayWindow returns the half-open UTC interval [startUTC, endUTC)
// that covers the user's local calendar day containing now. start is the
// user's local 00:00; end is the next local 00:00. Both are returned in
// UTC so they can be compared directly against the UTC timestamps stored
// in telemetry.db.
//
// tz is an IANA zone name (e.g. "America/New_York"). An empty or invalid
// tz falls back to UTC so a bad client value can never error the read
// path — it just anchors the window on UTC midnight instead.
//
// Across DST transitions the wall-clock day is 23 or 25 hours; because
// the bounds are explicit instants (next local midnight minus this local
// midnight) the interval stretches or shrinks automatically and the SUM
// queries still resolve correctly. endUTC is also the value surfaced as
// resets_at in the GET /me/usage response.
func LocalDayWindow(now time.Time, tz string) (startUTC, endUTC time.Time) {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}

	local := now.In(loc)
	// Build local midnight from the calendar date. Using time.Date with
	// the loaded location resolves the correct UTC offset for that wall
	// time, including DST, rather than assuming a fixed offset.
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	// AddDate(0,0,1) advances the calendar day, so the end is the next
	// local midnight even when the day is not exactly 24h (DST).
	end := start.AddDate(0, 0, 1)

	return start.UTC(), end.UTC()
}
