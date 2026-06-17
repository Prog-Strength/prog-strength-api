package daterange

import (
	"net/url"
	"strings"
	"testing"
	"time"

	_ "time/tzdata"
)

func TestDayBoundsUTC_UTCEquivalence(t *testing.T) {
	start, end, err := DayBoundsUTC("2025-06-03", time.UTC)
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
	loc, err := LoadTimezone("America/Denver")
	if err != nil {
		t.Fatalf("LoadTimezone error: %v", err)
	}

	start, end, err := DayBoundsUTC("2025-06-03", loc)
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
	loc, err := LoadTimezone("America/Denver")
	if err != nil {
		t.Fatalf("LoadTimezone error: %v", err)
	}

	start, end, err := DayBoundsUTC("2025-03-09", loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Clocks jump 2 AM -> 3 AM, losing an hour.
	if got := end.Sub(start); got != 23*time.Hour {
		t.Errorf("interval = %s, want 23h", got)
	}
}

func TestDayBoundsUTC_DSTFallBack(t *testing.T) {
	loc, err := LoadTimezone("America/Denver")
	if err != nil {
		t.Fatalf("LoadTimezone error: %v", err)
	}

	start, end, err := DayBoundsUTC("2025-11-02", loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Clocks fall 2 AM -> 1 AM, gaining an hour.
	if got := end.Sub(start); got != 25*time.Hour {
		t.Errorf("interval = %s, want 25h", got)
	}
}

func TestDayBoundsUTC_InvalidDate(t *testing.T) {
	_, _, err := DayBoundsUTC("2025-13-99", time.UTC)
	if err == nil {
		t.Fatal("expected error for invalid date, got nil")
	}
}

func TestLoadTimezone_InvalidName(t *testing.T) {
	_, err := LoadTimezone("Not/AZone")
	if err == nil {
		t.Fatal("expected error for invalid timezone, got nil")
	}
	if !strings.HasPrefix(err.Error(), "invalid timezone") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "invalid timezone")
	}
}

// TestParseQuery_SingleDayDenver is the regression lock for the planned-workout
// lookup bug: a Denver-local day must resolve to a 06:00Z..06:00Z window, NOT
// the 00:00Z..00:00Z window the model used to hand-build — which dropped
// evening sessions and pulled in the prior evening's.
func TestParseQuery_SingleDayDenver(t *testing.T) {
	q := url.Values{"timezone": {"America/Denver"}, "date": {"2026-06-17"}}
	start, end, loc, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loc.String() != "America/Denver" {
		t.Errorf("loc = %s, want America/Denver", loc)
	}
	wantStart := time.Date(2026, 6, 17, 6, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 18, 6, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start = %s, want %s", start.Format(time.RFC3339), wantStart.Format(time.RFC3339))
	}
	if !end.Equal(wantEnd) {
		t.Errorf("end = %s, want %s", end.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
}

func TestParseQuery_InclusiveRange(t *testing.T) {
	q := url.Values{
		"timezone":   {"America/Denver"},
		"start_date": {"2026-06-15"},
		"end_date":   {"2026-06-17"},
	}
	start, end, _, err := ParseQuery(q)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Start of 06-15 local through end of 06-17 local (exclusive) = start of
	// 06-18 local, both in UTC.
	wantStart := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 6, 18, 6, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Errorf("range = [%s, %s), want [%s, %s)",
			start.Format(time.RFC3339), end.Format(time.RFC3339),
			wantStart.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
}

func TestParseQuery_ContractErrors(t *testing.T) {
	cases := []struct {
		name string
		q    url.Values
		want string
	}{
		{"missing timezone", url.Values{"date": {"2026-06-17"}}, "timezone is required"},
		{"both date and range", url.Values{"timezone": {"UTC"}, "date": {"2026-06-17"}, "start_date": {"2026-06-15"}}, "supply either date or start_date+end_date, not both"},
		{"start without end", url.Values{"timezone": {"UTC"}, "start_date": {"2026-06-15"}}, "end_date is required when start_date is supplied"},
		{"end without start", url.Values{"timezone": {"UTC"}, "end_date": {"2026-06-17"}}, "start_date is required when end_date is supplied"},
		{"nothing", url.Values{"timezone": {"UTC"}}, "date or start_date+end_date is required"},
		{"reversed range", url.Values{"timezone": {"UTC"}, "start_date": {"2026-06-17"}, "end_date": {"2026-06-15"}}, "end_date must be on or after start_date"},
		{"bad timezone", url.Values{"timezone": {"Not/AZone"}, "date": {"2026-06-17"}}, "invalid timezone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := ParseQuery(tc.q)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("error = %q, want prefix %q", err.Error(), tc.want)
			}
		})
	}
}
