package activity

import "testing"

func TestActivityType_Valid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   ActivityType
		want bool
	}{
		{ActivityRunning, true},
		{ActivityWalking, true},
		{ActivityCycling, true},
		{ActivityOther, true},
		{ActivityType(""), false},
		{ActivityType("swimming"), false},
		{ActivityType("Running"), false}, // case-sensitive: enum values are lowercase
	}
	for _, c := range cases {
		if got := c.in.Valid(); got != c.want {
			t.Errorf("ActivityType(%q).Valid() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeActivityType_ManualTCX(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sport string
		want  ActivityType
	}{
		{"Running", ActivityRunning},
		{"running", ActivityRunning},
		{"  Running  ", ActivityRunning},
		{"Biking", ActivityCycling},
		{"biking", ActivityCycling},
		{"Other", ActivityOther},
		{"", ActivityOther},
		// TCX exporters in the wild occasionally emit unknown sports
		// (Walking isn't in the Garmin schema's enumerated values, for
		// example). Unknown falls through to Other; the row still lands.
		{"Walking", ActivityOther},
		{"Hiking", ActivityOther},
		{"garbage", ActivityOther},
	}
	for _, c := range cases {
		got := normalizeActivityType(c.sport, IngestManualTCX)
		if got != c.want {
			t.Errorf("normalizeActivityType(%q, ManualTCX) = %q, want %q", c.sport, got, c.want)
		}
	}
}

func TestNormalizeActivityType_GarminAPI_Panics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for IngestGarminAPI (not implemented), got none")
		}
	}()
	_ = normalizeActivityType("running", IngestGarminAPI)
}
