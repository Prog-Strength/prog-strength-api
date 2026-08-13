package calendarsync

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	"github.com/Prog-Strength/prog-strength-api/internal/daterange"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
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

// busyTitle is what an event Google returned with no summary renders as — a
// private event on a shared calendar, or a busy block. The user cannot see the
// title in Google either, so the tile should say so rather than draw an empty
// row.
const busyTitle = "Busy"

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

// EventsConfig is the [calendar_events] block, injected rather than imported
// so this package keeps its zero dependency on internal/config.
type EventsConfig struct {
	Enabled         bool
	CacheTTL        time.Duration
	MaxEventsPerDay int
}

// EventsWindow is one request's resolved date contract: the UTC half-open
// interval bracketing the user's local days, the zone those days are in, and
// the YYYY-MM-DD bounds themselves. StartDate and EndDate are both INCLUSIVE
// (EndUTC is the instant the EndDate ends), and Loc must be non-nil — build
// one with internal/daterange, which is what the handler does.
//
// The client NEVER computes timeMin/timeMax. internal/daterange's package doc
// explains why at length, and the DST case is not hypothetical here: a "week"
// is 167 or 169 hours twice a year, and a reader that assumes 7*24 drops or
// duplicates a day's events for every user not on UTC.
type EventsWindow struct {
	StartUTC  time.Time
	EndUTC    time.Time
	Loc       *time.Location
	StartDate string
	EndDate   string
}

// EventsServiceDeps carries the service's collaborators, mirroring
// ActivityServiceDeps.
type EventsServiceDeps struct {
	Conns  calendarconn.Repository
	Cipher *tokencrypt.Cipher
	Tokens *TokenSource
	Client CalendarClient
	Links  EventLinkRepository
	Config EventsConfig
	Now    func() time.Time
}

// EventsService reads a user's Google Calendar for the dashboard tile.
//
// WHY THIS LIVES IN calendarsync AND NOT A NEW PACKAGE. The unexported
// `connector` performs the five-step dance any Google call needs — load the
// connection, reject a revoked one, fetch the encrypted refresh token, decrypt
// it, mint an access token — plus the failure handling around it that is easy
// to get wrong: a refresh Google rejects must flip the connection to revoked,
// or the UI never learns to prompt for re-consent. Its own doc comment records
// that it was extracted from Service precisely because a second consumer
// appeared. A third consumer is the same argument. Putting the reader
// elsewhere would mean exporting `connector`, `grant`, and the
// revoke-on-refresh-failure behavior across a package boundary, in exchange
// for a package name that reads slightly better.
//
// NO NEW SCOPE. The granted scope is CalendarEventsScope (calendar.events),
// which is read AND write on events — events.list is already permitted — and
// the connection already resolves to `primary`. Every user connected today
// gets this the moment it deploys: no re-consent, no migration, no new secret.
//
// WHY A 429 MUST NOT REVOKE. Google rate-limits. A rate limit is not a revoked
// grant, and flipping the connection over one would present a re-consent
// prompt to a user whose authorization is perfectly good — and, because the
// write path shares that connection row, would ALSO stop their planned-workout
// sync. Only ErrTokenRejected (401/403) touches the connection; everything
// else degrades to "unavailable" and leaves it alone.
type EventsService struct {
	conns  calendarconn.Repository
	conn   *connector
	client CalendarClient
	links  EventLinkRepository
	cache  *eventsCache
	cfg    EventsConfig
	now    func() time.Time
}

func NewEventsService(d EventsServiceDeps) *EventsService {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &EventsService{
		conns:  d.Conns,
		conn:   &connector{conns: d.Conns, cipher: d.Cipher, tokens: d.Tokens, now: now},
		client: d.Client,
		links:  d.Links,
		cache:  newEventsCache(d.Config.CacheTTL, now),
		cfg:    d.Config,
		now:    now,
	}
}

// Events reads the user's calendar for the given window.
//
// It never returns an error: every failure is a status the tile can render
// calmly. not_connected in particular is the ORDINARY state of a user who
// never opted in — the tile's job there is to invite, not to handle an error —
// which mirrors ErrNotConnected's existing treatment in the write path, where
// it is explicitly not an error condition at all.
func (s *EventsService) Events(ctx context.Context, userID string, w EventsWindow) EventsResult {
	if !s.cfg.Enabled {
		observeEventRead(string(EventsStatusDisabled), cacheMiss, 0)
		return EventsResult{Status: EventsStatusDisabled}
	}

	key := eventsCacheKey{UserID: userID, Start: w.StartDate, End: w.EndDate, Timezone: w.Loc.String()}
	if days, ok := s.cache.get(key); ok {
		observeEventRead(string(EventsStatusOK), cacheHit, 0)
		return EventsResult{Status: EventsStatusOK, Days: days}
	}

	started := s.now()
	days, status := s.fetch(ctx, userID, w)
	observeEventRead(string(status), cacheMiss, s.now().Sub(started).Seconds())
	if status != EventsStatusOK {
		return EventsResult{Status: status}
	}
	s.cache.put(key, days)
	return EventsResult{Status: EventsStatusOK, Days: days}
}

// fetch does the uncached work: resolve the grant, list, filter, mark, group.
func (s *EventsService) fetch(ctx context.Context, userID string, w EventsWindow) ([]Day, EventsStatus) {
	g, err := s.conn.resolve(ctx, userID)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotConnected):
			return nil, EventsStatusNotConnected
		case errors.Is(err, ErrReconnectNeeded):
			// connector.resolve has already flipped the connection when the
			// refresh itself was rejected.
			return nil, EventsStatusReconnectNeeded
		default:
			return nil, EventsStatusUnavailable
		}
	}

	maxResults := s.maxResults(w)
	raw, err := s.client.ListEvents(ctx, g.AccessToken, g.CalendarID, w.StartUTC, w.EndUTC, maxResults)
	if err != nil {
		if errors.Is(err, ErrTokenRejected) {
			// 401/403 only. This is the one failure that means the grant is
			// gone; see the type's doc comment for why a 429 must not land here.
			_ = s.conns.SetStatus(ctx, userID, calendarconn.StatusRevoked, s.now())
			return nil, EventsStatusReconnectNeeded
		}
		return nil, EventsStatusUnavailable
	}

	// A link lookup failure degrades to an UNMARKED tile rather than no tile:
	// the events are the payload, the provenance mark is a garnish.
	links, err := s.links.LinksForUser(ctx, userID, w.StartUTC, w.EndUTC)
	if err != nil {
		links = nil
	}

	return s.group(raw, links, w), EventsStatusOK
}

// googleMaxResults is the largest page events.list will return.
const googleMaxResults = 2500

// maxResults bounds the Google page. Asking for the per-day cap across every
// day in the window means the per-day cap is what actually truncates, so the
// `truncated` counts stay meaningful; the ceiling is Google's own maximum.
//
// MaxEventsPerDay of 0 means "no per-day cap" — config.CalendarEventsConfig
// documents that zero value — so it asks for the whole page rather than the
// one event a literal 0 * days would buy: an uncapped tile showing a single
// event is the opposite of what the operator asked for. A window with no dates
// is degenerate and gets the same treatment. The multiply is guarded rather
// than clamped afterwards, since an overflowed product reads as "ask for one".
func (s *EventsService) maxResults(w EventsWindow) int {
	days := len(datesInWindow(w))
	if s.cfg.MaxEventsPerDay <= 0 || days <= 0 || s.cfg.MaxEventsPerDay > googleMaxResults/days {
		return googleMaxResults
	}
	return s.cfg.MaxEventsPerDay * days
}

// group buckets the listed events into dense local days.
func (s *EventsService) group(raw []ListedEvent, links map[string]EventLink, w EventsWindow) []Day {
	dates := datesInWindow(w)
	byDate := make(map[string][]Event, len(dates))

	for _, ev := range raw {
		// A meeting the user declined is one they are not attending; it is
		// noise on a tile this small. (Canceled events never arrive —
		// showDeleted=false.)
		if ev.Declined {
			continue
		}
		out := Event{
			ID:     ev.ID,
			Title:  ev.Summary,
			AllDay: ev.AllDay,
			Source: EventSourceGoogle,
		}
		if out.Title == "" {
			out.Title = busyTitle
		}
		if link, ok := links[ev.ID]; ok {
			out.Source = EventSourceProgStrength
			l := link
			out.Link = &l
		}

		if ev.AllDay {
			// Google's end.date is EXCLUSIVE, so a 14th->16th all-day event
			// covers the 14th and the 15th.
			for _, date := range allDayDates(ev, w.Loc) {
				day := out
				start, end, err := daterange.DayBoundsUTC(date, w.Loc)
				if err != nil {
					continue
				}
				day.Start, day.End = start, end
				byDate[date] = append(byDate[date], day)
			}
			continue
		}
		out.Start, out.End = ev.Start, ev.End
		date := out.Start.In(w.Loc).Format(dateLayout)
		byDate[date] = append(byDate[date], out)
	}

	days := make([]Day, 0, len(dates))
	for _, date := range dates {
		events := byDate[date]
		sortDayEvents(events)
		truncated := 0
		if s.cfg.MaxEventsPerDay > 0 && len(events) > s.cfg.MaxEventsPerDay {
			truncated = len(events) - s.cfg.MaxEventsPerDay
			events = events[:s.cfg.MaxEventsPerDay]
		}
		if events == nil {
			// Dense, and non-null on the wire.
			events = []Event{}
		}
		days = append(days, Day{Date: date, Truncated: truncated, Events: events})
	}
	return days
}

const dateLayout = "2006-01-02"

// datesInWindow lists every local calendar date the window covers, inclusive
// of both ends. Iteration is by AddDate in the user's zone, never by adding
// 24h — a DST day is 23 or 25 hours long.
func datesInWindow(w EventsWindow) []string {
	start, err := time.ParseInLocation(dateLayout, w.StartDate, w.Loc)
	if err != nil {
		return nil
	}
	end, err := time.ParseInLocation(dateLayout, w.EndDate, w.Loc)
	if err != nil {
		return nil
	}
	var dates []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format(dateLayout))
	}
	return dates
}

// allDayDates expands an all-day event across the dates it covers.
func allDayDates(ev ListedEvent, loc *time.Location) []string {
	start, err := time.ParseInLocation(dateLayout, ev.StartDate, loc)
	if err != nil {
		return nil
	}
	end, err := time.ParseInLocation(dateLayout, ev.EndDate, loc)
	if err != nil || !end.After(start) {
		// A malformed or single-day span still shows on its start date.
		return []string{ev.StartDate}
	}
	var dates []string
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format(dateLayout))
	}
	return dates
}

// sortDayEvents pins all-day events to the top, then orders the rest by start.
// The tile renders in this order and the truncation cap slices off the end, so
// the sort decides what a capped day keeps.
func sortDayEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].AllDay != events[j].AllDay {
			return events[i].AllDay
		}
		return events[i].Start.Before(events[j].Start)
	})
}
