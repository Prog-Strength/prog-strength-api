package calendarsync

import (
	"testing"
	"time"
)

func TestEventsCache_HitInsideTTL(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	c := newEventsCache(60*time.Second, func() time.Time { return now })
	key := eventsCacheKey{UserID: "u1", Start: "2026-08-10", End: "2026-08-17", Timezone: "UTC"}
	days := []Day{{Date: "2026-08-10"}}

	c.put(key, days, "America/Denver")
	if _, _, ok := c.get(key); !ok {
		t.Fatal("a read inside the TTL must hit")
	}

	now = now.Add(61 * time.Second)
	if _, _, ok := c.get(key); ok {
		t.Error("a read past the TTL must miss")
	}
}

func TestEventsCache_IsolatesUsersAndWindows(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	c := newEventsCache(60*time.Second, func() time.Time { return now })
	base := eventsCacheKey{UserID: "u1", Start: "2026-08-10", End: "2026-08-17", Timezone: "UTC"}
	c.put(base, []Day{{Date: "2026-08-10"}}, "America/Denver")

	other := base
	other.UserID = "u2"
	if _, _, ok := c.get(other); ok {
		t.Error("two users must never share a cache entry")
	}
	window := base
	window.End = "2026-08-18"
	if _, _, ok := c.get(window); ok {
		t.Error("a different window must not read another window's entry")
	}
	zone := base
	zone.Timezone = "America/New_York"
	if _, _, ok := c.get(zone); ok {
		t.Error("a different timezone groups days differently and must not share")
	}
}

func TestEventsCache_ReturnsACopy(t *testing.T) {
	now := time.Now()
	c := newEventsCache(time.Minute, func() time.Time { return now })
	key := eventsCacheKey{UserID: "u1", Start: "a", End: "b", Timezone: "UTC"}
	c.put(key, []Day{{Date: "2026-08-10", Events: []Event{
		{ID: "x", Link: &EventLink{Kind: LinkKindPlannedWorkout, ID: "pw_1"}},
	}}}, "America/Denver")

	got, _, _ := c.get(key)
	got[0].Events[0].ID = "mutated"
	got[0].Events[0].Link.ID = "mutated"

	again, _, _ := c.get(key)
	if again[0].Events[0].ID != "x" {
		t.Error("a caller mutating its result must not corrupt the cache")
	}
	// Link is a pointer: copying the Event alone would leave the cache and
	// every caller sharing one EventLink.
	if again[0].Events[0].Link.ID != "pw_1" {
		t.Errorf("link id = %q, want pw_1 — the link must be copied too", again[0].Events[0].Link.ID)
	}
}

// The cache holds one window per user, and that is what bounds it: the window
// slides with the calendar, so keying the map by the window would strand an
// entry per user per day that no later read is ever able to evict.
func TestEventsCache_KeepsOneEntryPerUser(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	c := newEventsCache(time.Minute, func() time.Time { return now })
	yesterday := eventsCacheKey{UserID: "u1", Start: "2026-08-11", End: "2026-08-18", Timezone: "UTC"}
	today := eventsCacheKey{UserID: "u1", Start: "2026-08-12", End: "2026-08-19", Timezone: "UTC"}

	c.put(yesterday, []Day{{Date: "2026-08-11"}}, "America/Denver")
	c.put(today, []Day{{Date: "2026-08-12"}}, "America/Denver")
	c.put(eventsCacheKey{UserID: "u2", Start: "2026-08-12", End: "2026-08-19", Timezone: "UTC"},
		[]Day{{Date: "2026-08-12"}}, "America/Denver")

	if got := len(c.entries); got != 2 {
		t.Errorf("cache holds %d entries, want one per user (2)", got)
	}
	// The superseded window must be gone — and gone as a MISS, never as
	// yesterday's days served under today's dates.
	if _, _, ok := c.get(yesterday); ok {
		t.Error("a superseded window must not still be served")
	}
	days, _, ok := c.get(today)
	if !ok {
		t.Fatal("the current window must still hit")
	}
	if days[0].Date != "2026-08-12" {
		t.Errorf("cached day = %q, want the window that was asked for", days[0].Date)
	}
}

// A free day is stored as an empty-but-present slice, and the cache must hand
// it back that way: the wire contract says days are dense and each one's
// `events` is a list, never null, and a cache hit is indistinguishable from a
// fresh fetch to the client.
func TestEventsCache_PreservesDenseEmptyDays(t *testing.T) {
	now := time.Now()
	c := newEventsCache(time.Minute, func() time.Time { return now })
	key := eventsCacheKey{UserID: "u1", Start: "a", End: "b", Timezone: "UTC"}
	c.put(key, []Day{{Date: "2026-08-10", Events: []Event{}}}, "America/Denver")

	got, _, ok := c.get(key)
	if !ok {
		t.Fatal("expected a hit")
	}
	if got[0].Events == nil {
		t.Error("a free day must survive the cache as an empty slice, not nil")
	}
}

// A cache the operator turned off (cache_ttl_seconds = 0) must never answer,
// so every read goes to Google.
func TestEventsCache_ZeroTTLNeverStores(t *testing.T) {
	c := newEventsCache(0, time.Now)
	key := eventsCacheKey{UserID: "u1", Start: "a", End: "b", Timezone: "UTC"}

	c.put(key, []Day{{Date: "2026-08-10"}}, "America/Denver")
	if _, _, ok := c.get(key); ok {
		t.Error("a zero TTL must never serve a cached window")
	}
}
