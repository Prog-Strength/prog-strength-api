package activity

import (
	"strings"
	"testing"
)

// ptrF, ptrInt, and ptrStr come from sqlite_repository_test.go — this
// package shares one set of pointer helpers across its test files.

// sectionByHeading finds a rendered section, reporting absence rather than
// panicking so tests can assert a section is missing.
func sectionByHeading(m CalendarManifest, heading string) (ManifestSection, bool) {
	for _, s := range m.Sections {
		if s.Heading == heading {
			return s, true
		}
	}
	return ManifestSection{}, false
}

func TestEnduranceCalendarEvent_HeadlineCarriesDistanceDurationAndPace(t *testing.T) {
	render := enduranceCalendarEvent(ActivityRunning)
	a := Activity{
		ID:              "act_1",
		ActivityType:    ActivityRunning,
		DistanceMeters:  8368.6, // 5.2 mi
		DurationSeconds: 2472,   // 41:12
		AvgPaceSecPerKm: ptrF(295.4),
	}

	got := render(a, nil)
	if got.Headline != "5.2 mi · 41:12 · 7:55/mi" {
		t.Fatalf("Headline = %q, want %q", got.Headline, "5.2 mi · 41:12 · 7:55/mi")
	}
	if got.Title != "Run" {
		t.Fatalf("Title = %q, want the type label %q", got.Title, "Run")
	}
}

func TestEnduranceCalendarEvent_NameOverridesTheTypeLabel(t *testing.T) {
	render := enduranceCalendarEvent(ActivityRunning)
	got := render(Activity{ActivityType: ActivityRunning, Name: ptrStr("Threshold Run")}, nil)
	if got.Title != "Threshold Run" {
		t.Fatalf("Title = %q, want the activity's name", got.Title)
	}
}

// Details are authoritative (they carry the calibrated distance), so the
// calendar must not report the raw base-row value the card already ignores.
func TestEnduranceCalendarEvent_PrefersLoadedDetailsForDistanceAndPace(t *testing.T) {
	render := enduranceCalendarEvent(ActivityRunning)
	a := Activity{
		ActivityType:    ActivityRunning,
		DistanceMeters:  1609.344, // stale base row: 1.0 mi
		DurationSeconds: 600,
		AvgPaceSecPerKm: ptrF(600),
	}
	d := &EnduranceDetails{DistanceMeters: 3218.688, AvgPaceSecPerKm: ptrF(186.4)} // 2.0 mi

	got := render(a, d)
	if !strings.HasPrefix(got.Headline, "2.0 mi") {
		t.Fatalf("Headline = %q, want the calibrated 2.0 mi from details", got.Headline)
	}
	if !strings.Contains(got.Headline, "5:00/mi") {
		t.Fatalf("Headline = %q, want the details' pace", got.Headline)
	}
}

func TestEnduranceCalendarEvent_HeartRateSection(t *testing.T) {
	render := enduranceCalendarEvent(ActivityRunning)
	a := Activity{ActivityType: ActivityRunning, AvgHeartRateBpm: ptrInt(162), MaxHeartRateBpm: ptrInt(178)}

	sec, ok := sectionByHeading(render(a, nil), "Heart rate")
	if !ok {
		t.Fatal("no Heart rate section, want one")
	}
	if sec.Lines[0] != "Avg 162 bpm · Max 178 bpm" {
		t.Fatalf("HR line = %q", sec.Lines[0])
	}
}

// An indoor run with no strap must not render an empty or zeroed HR block.
func TestEnduranceCalendarEvent_OmitsSectionsWithNoData(t *testing.T) {
	render := enduranceCalendarEvent(ActivityRunning)
	got := render(Activity{ActivityType: ActivityRunning, DistanceMeters: 5000, DurationSeconds: 1800}, nil)

	for _, heading := range []string{"Heart rate", "Elevation", "Best efforts", "Notes"} {
		if _, ok := sectionByHeading(got, heading); ok {
			t.Fatalf("rendered a %q section with no underlying data", heading)
		}
	}
}

func TestEnduranceCalendarEvent_ElevationSectionRendersGainAndLoss(t *testing.T) {
	render := enduranceCalendarEvent(ActivityHiking)
	a := Activity{
		ActivityType:        ActivityHiking,
		ElevationGainMeters: ptrF(1051.6),
		ElevationLossMeters: ptrF(1000),
	}

	sec, ok := sectionByHeading(render(a, nil), "Elevation")
	if !ok {
		t.Fatal("no Elevation section, want one")
	}
	if !strings.Contains(sec.Lines[0], "Gain 3,450 ft ↑") {
		t.Fatalf("elevation line = %q, want the gain in feet", sec.Lines[0])
	}
	if !strings.Contains(sec.Lines[0], "Loss 3,281 ft") {
		t.Fatalf("elevation line = %q, want the loss in feet", sec.Lines[0])
	}
}

// Best efforts must render shortest-first regardless of the order the
// repository handed them back.
func TestEnduranceCalendarEvent_BestEffortsRenderInStandardDistanceOrder(t *testing.T) {
	render := enduranceCalendarEvent(ActivityRunning)
	a := Activity{
		ActivityType: ActivityRunning,
		BestEfforts: []ActivityBestEffort{
			{DistanceKey: "5k", DurationSeconds: 1458},
			{DistanceKey: "1mi", DurationSeconds: 451},
		},
	}

	sec, ok := sectionByHeading(render(a, nil), "Best efforts")
	if !ok {
		t.Fatal("no Best efforts section, want one")
	}
	if len(sec.Lines) != 2 {
		t.Fatalf("lines = %v, want 2", sec.Lines)
	}
	if sec.Lines[0] != "1 Mile — 7:31" {
		t.Fatalf("first line = %q, want the 1 Mile effort first", sec.Lines[0])
	}
	if sec.Lines[1] != "5K — 24:18" {
		t.Fatalf("second line = %q", sec.Lines[1])
	}
}

func TestEnduranceCalendarEvent_NotesSectionPreservesLineBreaks(t *testing.T) {
	render := enduranceCalendarEvent(ActivityRunning)
	a := Activity{ActivityType: ActivityRunning, Notes: ptrStr("Felt strong.\nNew shoes.")}

	sec, ok := sectionByHeading(render(a, nil), "Notes")
	if !ok {
		t.Fatal("no Notes section, want one")
	}
	if len(sec.Lines) != 2 || sec.Lines[1] != "New shoes." {
		t.Fatalf("notes lines = %v, want the two lines preserved", sec.Lines)
	}
}

// A duration-only entry (the ActivityOther shape) must not lead with "0.0 mi".
func TestEnduranceCalendarEvent_ZeroDistanceDropsTheDistanceClause(t *testing.T) {
	render := enduranceCalendarEvent(ActivityOther)
	got := render(Activity{ActivityType: ActivityOther, DurationSeconds: 1800}, nil)
	if got.Headline != "30:00" {
		t.Fatalf("Headline = %q, want duration only", got.Headline)
	}
}
