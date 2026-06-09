package usage

import (
	"testing"
	"time"
)

func TestLocalDayWindow_UTC(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 22, 10, 0, time.UTC)
	start, end := LocalDayWindow(now, "UTC")

	wantStart := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("got [%s, %s) want [%s, %s)", start, end, wantStart, wantEnd)
	}
	if d := end.Sub(start); d != 24*time.Hour {
		t.Fatalf("interval: got %s want 24h", d)
	}
}

func TestLocalDayWindow_EmptyTzFallsBackToUTC(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	start, end := LocalDayWindow(now, "")

	wantStart := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("empty tz: got [%s, %s) want UTC day", start, end)
	}
}

func TestLocalDayWindow_InvalidTzFallsBackToUTC(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	start, end := LocalDayWindow(now, "Not/AZone")

	wantStart := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || end.Sub(start) != 24*time.Hour {
		t.Fatalf("invalid tz: got [%s, %s) want UTC day", start, end)
	}
}

func TestLocalDayWindow_FixedOffsetZone(t *testing.T) {
	// New York is UTC-4 on 2026-06-09 (EDT). A moment at 02:00 UTC is
	// still the previous local calendar day (22:00 the night before).
	now := time.Date(2026, 6, 9, 2, 0, 0, 0, time.UTC)
	start, end := LocalDayWindow(now, "America/New_York")

	// Local date is 2026-06-08; local midnight is 04:00 UTC.
	wantStart := time.Date(2026, 6, 8, 4, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 9, 4, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("NY window: got [%s, %s) want [%s, %s)", start, end, wantStart, wantEnd)
	}
	if d := end.Sub(start); d != 24*time.Hour {
		t.Fatalf("NY interval: got %s want 24h", d)
	}
}

func TestLocalDayWindow_DSTSpringForward(t *testing.T) {
	// US DST spring-forward 2026: 02:00 -> 03:00 on Sunday March 8.
	// That local calendar day is only 23 hours long.
	now := time.Date(2026, 3, 8, 18, 0, 0, 0, time.UTC)
	start, end := LocalDayWindow(now, "America/New_York")

	if d := end.Sub(start); d != 23*time.Hour {
		t.Fatalf("spring-forward interval: got %s want 23h", d)
	}
}

func TestLocalDayWindow_DSTFallBack(t *testing.T) {
	// US DST fall-back 2026: 02:00 -> 01:00 on Sunday November 1.
	// That local calendar day is 25 hours long.
	now := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	start, end := LocalDayWindow(now, "America/New_York")

	if d := end.Sub(start); d != 25*time.Hour {
		t.Fatalf("fall-back interval: got %s want 25h", d)
	}
}

func TestLocalDayWindow_EndIsNextLocalMidnight(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	_, end := LocalDayWindow(now, "America/New_York")

	loc, _ := time.LoadLocation("America/New_York")
	endLocal := end.In(loc)
	if endLocal.Hour() != 0 || endLocal.Minute() != 0 || endLocal.Second() != 0 {
		t.Fatalf("end is not local midnight: %s", endLocal)
	}
}
