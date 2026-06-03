package nutrition

import (
	"fmt"
	"time"
)

// dayBoundsUTC returns the UTC instants that bracket the given calendar
// day in loc. The end bound is exclusive, matching SQL BETWEEN-like
// half-open range semantics used downstream. On DST transition days the
// returned interval is 23h or 25h respectively; callers should not
// assume 24 hours.
func dayBoundsUTC(date string, loc *time.Location) (time.Time, time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q: %w", date, err)
	}
	return d.UTC(), d.AddDate(0, 0, 1).UTC(), nil
}

// loadTimezone wraps time.LoadLocation with a consistent error shape so
// handlers produce identical 400 messages for an unknown/malformed IANA name.
func loadTimezone(name string) (*time.Location, error) {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %s: %v", name, err)
	}
	return loc, nil
}
