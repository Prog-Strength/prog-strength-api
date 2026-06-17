// Package daterange resolves the timezone-aware date contract shared by the
// API's date-windowed list endpoints (nutrition log, daily macros, planned
// workouts): a required IANA `timezone` plus either a single `date` or a
// `start_date`+`end_date` range, all YYYY-MM-DD, converted into the UTC
// half-open interval [start, end) that brackets those user-local calendar
// days.
//
// Centralizing the conversion keeps every endpoint agreeing on where a day
// begins and ends — the alternative, letting each caller (or worse, an LLM
// composing tool args) build UTC timestamps itself, silently drops or
// double-counts sessions near the local-midnight boundary for any user who
// isn't on UTC.
package daterange

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// LoadTimezone wraps time.LoadLocation with a consistent error shape so every
// endpoint produces an identical 400 message for an unknown/malformed IANA
// name. (The API binary embeds the zoneinfo DB via a time/tzdata blank import
// in cmd/api, so this resolves the same in a scratch container as on a dev box.)
func LoadTimezone(name string) (*time.Location, error) {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %s: %w", name, err)
	}
	return loc, nil
}

// DayBoundsUTC returns the UTC instants that bracket the given calendar day in
// loc. The end bound is exclusive, matching the half-open range semantics used
// downstream. On DST transition days the returned interval is 23h or 25h
// respectively; callers must not assume 24 hours.
func DayBoundsUTC(date string, loc *time.Location) (time.Time, time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q: %w", date, err)
	}
	return d.UTC(), d.AddDate(0, 0, 1).UTC(), nil
}

// ParseQuery resolves the timezone-aware date contract from query params.
// Returns the UTC half-open interval [start, end) bracketing the user-local
// calendar day(s), plus the resolved *time.Location for downstream local-date
// grouping. Errors are 400-grade with stable messages — handlers forward
// err.Error() verbatim, so the strings here are the contract.
func ParseQuery(q url.Values) (time.Time, time.Time, *time.Location, error) {
	tzName := q.Get("timezone")
	if tzName == "" {
		return time.Time{}, time.Time{}, nil, errors.New("timezone is required")
	}
	loc, err := LoadTimezone(tzName)
	if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}

	date := q.Get("date")
	startDate := q.Get("start_date")
	endDate := q.Get("end_date")

	switch {
	case date != "" && (startDate != "" || endDate != ""):
		return time.Time{}, time.Time{}, nil, errors.New("supply either date or start_date+end_date, not both")
	case startDate != "" && endDate == "":
		return time.Time{}, time.Time{}, nil, errors.New("end_date is required when start_date is supplied")
	case endDate != "" && startDate == "":
		return time.Time{}, time.Time{}, nil, errors.New("start_date is required when end_date is supplied")
	case date == "" && startDate == "" && endDate == "":
		return time.Time{}, time.Time{}, nil, errors.New("date or start_date+end_date is required")
	}

	if date != "" {
		start, end, dayErr := DayBoundsUTC(date, loc)
		if dayErr != nil {
			return time.Time{}, time.Time{}, nil, dayErr
		}
		return start, end, loc, nil
	}

	start, _, err := DayBoundsUTC(startDate, loc)
	if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	endStart, end, err := DayBoundsUTC(endDate, loc)
	if err != nil {
		return time.Time{}, time.Time{}, nil, err
	}
	if endStart.Before(start) {
		return time.Time{}, time.Time{}, nil, errors.New("end_date must be on or after start_date")
	}
	return start, end, loc, nil
}
