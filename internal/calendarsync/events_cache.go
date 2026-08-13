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

type eventsCacheEntry struct {
	days     []Day
	storedAt time.Time
}

// eventsCache is a per-user in-memory TTL cache and nothing else.
//
// Its only job is to keep dashboard remounts and slide changes off Google.
// There is deliberately no durable cache, no budget ledger, no daily ceiling,
// and no stale serving — all of which the weather integration carries because
// OpenWeather BILLS PER CALL. Google Calendar's API is free at a quota of one
// million queries a day, so a cache miss here is a free retry and importing
// that machinery would be cargo-culting a solution to a problem this
// integration does not have.
type eventsCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[eventsCacheKey]eventsCacheEntry
}

func newEventsCache(ttl time.Duration, now func() time.Time) *eventsCache {
	if now == nil {
		now = time.Now
	}
	return &eventsCache{ttl: ttl, now: now, entries: make(map[eventsCacheKey]eventsCacheEntry)}
}

// get returns a deep copy of the cached days, or false when absent or expired.
// The copy is the point: the caller renders and may sort or truncate what it
// gets back, and a shared slice would let one request corrupt the next.
func (c *eventsCache) get(key eventsCacheKey) ([]Day, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().Sub(entry.storedAt) >= c.ttl {
		delete(c.entries, key)
		return nil, false
	}
	return cloneDays(entry.days), true
}

func (c *eventsCache) put(key eventsCacheKey, days []Day) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = eventsCacheEntry{days: cloneDays(days), storedAt: c.now()}
}

// cloneDays copies the days and each day's event slice. A day with no events
// keeps its empty-but-present slice rather than collapsing to nil (which
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
		out[i].Events = append(make([]Event, 0, len(d.Events)), d.Events...)
	}
	return out
}
