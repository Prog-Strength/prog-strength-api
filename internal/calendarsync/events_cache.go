package calendarsync

import (
	"sync"
	"time"
)

// eventsCacheKey identifies one user's fetched window. Every field is part of
// the identity: the timezone decides which local day an instant lands on, so
// two zones over the same dates are genuinely different answers.
type eventsCacheKey struct {
	UserID   string
	Start    string
	End      string
	Timezone string
}

// eventsCacheEntry is one user's single live window. The key is stored
// ALONGSIDE the days, not just used to find them: the map is keyed by user, so
// a read has to confirm the entry it found describes the window that was asked
// for, or a user who slid the tile forward would be served yesterday's days
// under today's dates.
type eventsCacheEntry struct {
	key      eventsCacheKey
	days     []Day
	storedAt time.Time
}

// eventsCache holds AT MOST ONE fetched window per user and nothing else.
//
// Its only job is to keep dashboard remounts and slide changes off Google.
// There is deliberately no durable cache, no budget ledger, no daily ceiling,
// and no stale serving — all of which the weather integration carries because
// OpenWeather BILLS PER CALL. Google Calendar's API is free at a quota of one
// million queries a day, so a cache miss here is a free retry and importing
// that machinery would be cargo-culting a solution to a problem this
// integration does not have.
//
// ONE ENTRY PER USER IS WHAT BOUNDS IT. Keying by the whole window instead
// would grow without limit: the window slides with the calendar, so every
// active user would strand roughly one entry per day, and entries are only
// ever evicted by a read that lands on an expired one — a read yesterday's
// dates will never get again. A user only ever asks for the window their
// dashboard is showing, so a second window for that user means the first one
// is dead, and replacing it makes the leak structurally impossible rather than
// merely slow.
type eventsCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]eventsCacheEntry // user id → that user's one live window
}

func newEventsCache(ttl time.Duration, now func() time.Time) *eventsCache {
	if now == nil {
		now = time.Now
	}
	return &eventsCache{ttl: ttl, now: now, entries: make(map[string]eventsCacheEntry)}
}

// get returns a copy of the cached days, or false when the user has no entry,
// their entry is for a different window, or it has expired. The copy is the
// point: the caller renders and may sort or truncate what it gets back, and a
// shared slice would let one request corrupt the next.
func (c *eventsCache) get(key eventsCacheKey) ([]Day, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key.UserID]
	if !ok || entry.key != key {
		return nil, false
	}
	if c.now().Sub(entry.storedAt) >= c.ttl {
		delete(c.entries, key.UserID)
		return nil, false
	}
	return cloneDays(entry.days), true
}

// put replaces whatever window this user had cached.
func (c *eventsCache) put(key eventsCacheKey, days []Day) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key.UserID] = eventsCacheEntry{key: key, days: cloneDays(days), storedAt: c.now()}
}

// cloneDays copies the days, each day's event slice, and each event's Link —
// everything reachable from a Day that is not immutable, so a caller can do
// what it likes with what it got. A day with no events keeps its
// empty-but-present slice rather than collapsing to nil (which
// `append([]Event(nil), ...)` would do): days are dense and each one's events
// render as `[]` on the wire, and a cache hit must be indistinguishable from
// the fetch that filled it.
func cloneDays(days []Day) []Day {
	out := make([]Day, len(days))
	for i, d := range days {
		out[i] = d
		if d.Events == nil {
			continue
		}
		events := append(make([]Event, 0, len(d.Events)), d.Events...)
		for j := range events {
			if events[j].Link == nil {
				continue
			}
			link := *events[j].Link
			events[j].Link = &link
		}
		out[i].Events = events
	}
	return out
}
