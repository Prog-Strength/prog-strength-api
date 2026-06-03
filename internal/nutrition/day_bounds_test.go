package nutrition

import (
	"strings"
	"testing"
	"time"

	_ "time/tzdata"
)

func TestDayBoundsUTC_UTCEquivalence(t *testing.T) {
	start, end, err := dayBoundsUTC("2025-06-03", time.UTC)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantStart := time.Date(2025, 6, 3, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2025, 6, 4, 0, 0, 0, 0, time.UTC)

	if !start.Equal(wantStart) {
		t.Errorf("start = %s, want %s", start.Format(time.RFC3339), wantStart.Format(time.RFC3339))
	}
	if !end.Equal(wantEnd) {
		t.Errorf("end = %s, want %s", end.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
	if got := end.Sub(start); got != 24*time.Hour {
		t.Errorf("interval = %s, want 24h", got)
	}
}

func TestDayBoundsUTC_DenverNonDST(t *testing.T) {
	loc, err := loadTimezone("America/Denver")
	if err != nil {
		t.Fatalf("loadTimezone error: %v", err)
	}

	start, end, err := dayBoundsUTC("2025-06-03", loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// MDT is UTC-6, so local midnight maps to 06:00 UTC.
	wantStart := time.Date(2025, 6, 3, 6, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2025, 6, 4, 6, 0, 0, 0, time.UTC)

	if !start.Equal(wantStart) {
		t.Errorf("start = %s, want %s", start.Format(time.RFC3339), wantStart.Format(time.RFC3339))
	}
	if !end.Equal(wantEnd) {
		t.Errorf("end = %s, want %s", end.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
	if got := end.Sub(start); got != 24*time.Hour {
		t.Errorf("interval = %s, want 24h", got)
	}
}

func TestDayBoundsUTC_DSTSpringForward(t *testing.T) {
	loc, err := loadTimezone("America/Denver")
	if err != nil {
		t.Fatalf("loadTimezone error: %v", err)
	}

	start, end, err := dayBoundsUTC("2025-03-09", loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Clocks jump 2 AM -> 3 AM, losing an hour.
	if got := end.Sub(start); got != 23*time.Hour {
		t.Errorf("interval = %s, want 23h", got)
	}
}

func TestDayBoundsUTC_DSTFallBack(t *testing.T) {
	loc, err := loadTimezone("America/Denver")
	if err != nil {
		t.Fatalf("loadTimezone error: %v", err)
	}

	start, end, err := dayBoundsUTC("2025-11-02", loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Clocks fall 2 AM -> 1 AM, gaining an hour.
	if got := end.Sub(start); got != 25*time.Hour {
		t.Errorf("interval = %s, want 25h", got)
	}
}

func TestDayBoundsUTC_InvalidDate(t *testing.T) {
	_, _, err := dayBoundsUTC("2025-13-99", time.UTC)
	if err == nil {
		t.Fatal("expected error for invalid date, got nil")
	}
}

func TestLoadTimezone_InvalidName(t *testing.T) {
	_, err := loadTimezone("Not/AZone")
	if err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}
	if !strings.HasPrefix(err.Error(), "invalid timezone") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "invalid timezone")
	}
}
