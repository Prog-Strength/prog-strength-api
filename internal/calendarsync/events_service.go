package calendarsync

import (
	"time"
)

// EventsStatus is the closed degradation set, mirroring weather's vocabulary.
// Every value renders as a calm muted line or a CTA on the tile — never an
// error banner.
type EventsStatus string

const (
	EventsStatusOK              EventsStatus = "ok"
	EventsStatusNotConnected    EventsStatus = "not_connected"
	EventsStatusReconnectNeeded EventsStatus = "reconnect_needed"
	EventsStatusDisabled        EventsStatus = "disabled"
	EventsStatusUnavailable     EventsStatus = "unavailable"
)

// Event sources. prog_strength marks an event Prog Strength itself wrote.
const (
	EventSourceProgStrength = "prog_strength"
	EventSourceGoogle       = "google"
)

// Event is one calendar entry on the wire.
type Event struct {
	ID     string
	Title  string
	Start  time.Time
	End    time.Time
	AllDay bool
	Source string
	Link   *EventLink
}

// Day is one local calendar date. days is DENSE — every date in the requested
// window appears, with an empty Events slice for a free day — because a client
// that has to distinguish "no events" from "day missing from the payload" will
// eventually get it wrong, and the week strip needs seven columns regardless.
type Day struct {
	Date string
	// Truncated is how many events MaxEventsPerDay cut from this day. It
	// exists so the cap is never silent: the week strip prints
	// len(events)+truncated, so a conference day with 60 entries reports sixty
	// rather than the fifty that fit.
	Truncated int
	Events    []Event
}

// EventsResult is what the handler renders.
type EventsResult struct {
	Status EventsStatus
	Days   []Day
}
