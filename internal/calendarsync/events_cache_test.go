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

	c.put(key, days)
	if _, ok := c.get(key); !ok {
		t.Fatal("a read inside the TTL must hit")
	}

	now = now.Add(61 * time.Second)
	if _, ok := c.get(key); ok {
		t.Error("a read past the TTL must miss")
	}
}

func TestEventsCache_IsolatesUsersAndWindows(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	c := newEventsCache(60*time.Second, func() time.Time { return now })
	base := eventsCacheKey{UserID: "u1", Start: "2026-08-10", End: "2026-08-17", Timezone: "UTC"}
	c.put(base, []Day{{Date: "2026-08-10"}})

	other := base
	other.UserID = "u2"
	if _, ok := c.get(other); ok {
		t.Error("two users must never share a cache entry")
	}
	window := base
	window.End = "2026-08-18"
	if _, ok := c.get(window); ok {
		t.Error("a different window must not read another window's entry")
	}
	zone := base
	zone.Timezone = "America/New_York"
	if _, ok := c.get(zone); ok {
		t.Error("a different timezone groups days differently and must not share")
	}
}

func TestEventsCache_ReturnsACopy(t *testing.T) {
	now := time.Now()
	c := newEventsCache(time.Minute, func() time.Time { return now })
	key := eventsCacheKey{UserID: "u1", Start: "a", End: "b", Timezone: "UTC"}
	c.put(key, []Day{{Date: "2026-08-10", Events: []Event{{ID: "x"}}}})

	got, _ := c.get(key)
	got[0].Events[0].ID = "mutated"

	again, _ := c.get(key)
	if again[0].Events[0].ID != "x" {
		t.Error("a caller mutating its result must not corrupt the cache")
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
	c.put(key, []Day{{Date: "2026-08-10", Events: []Event{}}})

	got, ok := c.get(key)
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

	c.put(key, []Day{{Date: "2026-08-10"}})
	if _, ok := c.get(key); ok {
		t.Error("a zero TTL must never serve a cached window")
	}
}
