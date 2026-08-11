package dashboard

import (
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/whoopsleep"
)

func msPtr(v int64) *int64 { return &v }

// dayBefore returns the local YYYY-MM-DD n days before testNow.
func dayBefore(n int) string {
	return testNow.AddDate(0, 0, -n).Format("2006-01-02")
}

// night builds a scored non-nap record on date with the given in-bed duration.
func night(date, id string, inBed int64, endedAt string) whoopsleep.Entry {
	return whoopsleep.Entry{
		WhoopSleepID: id, Date: date, EndedAt: endedAt, ScoreState: "SCORED",
		InBedMilli: msPtr(inBed),
	}
}

// --- pickNight --------------------------------------------------------------

func TestPickNight_SingleNonNapRecord(t *testing.T) {
	recs := []whoopsleep.Entry{night("2026-06-17", "a", 28800000, "2026-06-17T07:00:00Z")}
	got := pickNight(recs)
	if got == nil || got.WhoopSleepID != "a" {
		t.Fatalf("pickNight = %+v, want the single non-nap record", got)
	}
}

func TestPickNight_NapsAreNeverSelected(t *testing.T) {
	nap := night("2026-06-17", "nap", 3600000, "2026-06-17T14:00:00Z")
	nap.IsNap = true
	other := night("2026-06-17", "nap2", 7200000, "2026-06-17T16:00:00Z")
	other.IsNap = true

	if got := pickNight([]whoopsleep.Entry{nap, other}); got != nil {
		t.Fatalf("pickNight = %+v, want nil for a nap-only date", got)
	}
	// A nap never outranks a real night even when it is longer in bed.
	long := night("2026-06-17", "nap-long", 99999999, "2026-06-17T18:00:00Z")
	long.IsNap = true
	actual := night("2026-06-17", "night", 1000, "2026-06-17T07:00:00Z")
	if got := pickNight([]whoopsleep.Entry{long, actual}); got == nil || got.WhoopSleepID != "night" {
		t.Fatalf("pickNight = %+v, want the non-nap record", got)
	}
}

func TestPickNight_NoRecords(t *testing.T) {
	if got := pickNight(nil); got != nil {
		t.Fatalf("pickNight(nil) = %+v, want nil", got)
	}
	if got := pickNight([]whoopsleep.Entry{}); got != nil {
		t.Fatalf("pickNight(empty) = %+v, want nil", got)
	}
}

func TestPickNight_LongestInBedWins(t *testing.T) {
	short := night("2026-06-17", "short", 10800000, "2026-06-17T09:00:00Z")
	long := night("2026-06-17", "long", 25200000, "2026-06-17T06:00:00Z")
	// Ordered so the shorter (and later-ending) record comes first: duration
	// must beat both list position and the ended_at tie-break.
	if got := pickNight([]whoopsleep.Entry{short, long}); got == nil || got.WhoopSleepID != "long" {
		t.Fatalf("pickNight = %+v, want the longest in-bed record", got)
	}
}

func TestPickNight_EqualInBedBreaksOnLatestEndedAt(t *testing.T) {
	early := night("2026-06-17", "early", 25200000, "2026-06-17T05:00:00Z")
	late := night("2026-06-17", "late", 25200000, "2026-06-17T08:30:00Z")
	if got := pickNight([]whoopsleep.Entry{late, early}); got == nil || got.WhoopSleepID != "late" {
		t.Fatalf("pickNight = %+v, want the latest-ending record", got)
	}
	if got := pickNight([]whoopsleep.Entry{early, late}); got == nil || got.WhoopSleepID != "late" {
		t.Fatalf("pickNight (reordered) = %+v, want the latest-ending record", got)
	}
}

// Two records equal on in-bed duration AND ended_at should not exist, but
// ListRange's ordering only became a total order once whoop_sleep_id joined it,
// so the selection rule needs the same last tie-break: without it the winner is
// whichever row SQLite happened to return first.
func TestPickNight_FullyEqualRecordsBreakOnWhoopSleepID(t *testing.T) {
	a := night("2026-06-17", "aaa", 25200000, "2026-06-17T07:00:00Z")
	b := night("2026-06-17", "bbb", 25200000, "2026-06-17T07:00:00Z")
	if got := pickNight([]whoopsleep.Entry{a, b}); got == nil || got.WhoopSleepID != "bbb" {
		t.Fatalf("pickNight = %+v, want bbb", got)
	}
	if got := pickNight([]whoopsleep.Entry{b, a}); got == nil || got.WhoopSleepID != "bbb" {
		t.Fatalf("pickNight (reordered) = %+v, want bbb regardless of list order", got)
	}
}

// An unscored record has no in-bed duration AT ALL — not a zero one. It loses to
// any record that has one, and two of them fall through to the ended_at
// tie-break rather than comparing as equal-at-zero.
func TestPickNight_NilInBedLosesAndFallsThroughToEndedAt(t *testing.T) {
	unscored := whoopsleep.Entry{
		WhoopSleepID: "pending", Date: "2026-06-17",
		EndedAt: "2026-06-17T09:00:00Z", ScoreState: "PENDING",
	}
	scored := night("2026-06-17", "scored", 1, "2026-06-17T06:00:00Z")
	if got := pickNight([]whoopsleep.Entry{unscored, scored}); got == nil || got.WhoopSleepID != "scored" {
		t.Fatalf("pickNight = %+v, want the record that has an in-bed duration", got)
	}

	otherUnscored := whoopsleep.Entry{
		WhoopSleepID: "pending-late", Date: "2026-06-17",
		EndedAt: "2026-06-17T10:00:00Z", ScoreState: "PENDING",
	}
	got := pickNight([]whoopsleep.Entry{unscored, otherUnscored})
	if got == nil || got.WhoopSleepID != "pending-late" {
		t.Fatalf("pickNight = %+v, want the latest-ending unscored record", got)
	}
}

// --- buildSleep -------------------------------------------------------------

func TestBuildSleep_NightsAreDateAlignedWithNullGaps(t *testing.T) {
	loc := time.UTC
	entries := []whoopsleep.Entry{
		night(dayBefore(0), "today", 28800000, dayBefore(0)+"T07:00:00Z"),
		night(dayBefore(1), "yday", 27000000, dayBefore(1)+"T07:00:00Z"),
		// dayBefore(2) deliberately missing — the interior gap.
		night(dayBefore(3), "d3", 26000000, dayBefore(3)+"T07:00:00Z"),
	}

	got := buildSleep(entries, testNow, loc)
	if got == nil {
		t.Fatal("buildSleep = nil, want a section")
	}
	if len(got.Nights) != sleepWindowDays {
		t.Fatalf("len(Nights) = %d, want %d (every date present)", len(got.Nights), sleepWindowDays)
	}
	// Oldest→newest, one date per day, ending today.
	for i := range got.Nights {
		want := dayBefore(sleepWindowDays - 1 - i)
		if got.Nights[i].Date != want {
			t.Fatalf("Nights[%d].Date = %q, want %q", i, got.Nights[i].Date, want)
		}
	}

	last := got.Nights[len(got.Nights)-1]
	if last.InBedMilli == nil || *last.InBedMilli != 28800000 {
		t.Errorf("today in_bed_milli = %v, want 28800000", last.InBedMilli)
	}
	// The interior gap is PRESENT with null metrics — never omitted, never
	// zero-filled: a night with no data and a night of zero sleep differ.
	gap := got.Nights[len(got.Nights)-3]
	if gap.Date != dayBefore(2) {
		t.Fatalf("gap date = %q, want %q", gap.Date, dayBefore(2))
	}
	if gap.InBedMilli != nil || gap.PerformancePct != nil || gap.RespiratoryRate != nil {
		t.Errorf("gap night carries metrics, want all null: %+v", gap)
	}
	// The days flanking the gap still carry their data.
	if got.Nights[len(got.Nights)-2].InBedMilli == nil {
		t.Error("yesterday lost its metrics")
	}
	if got.Nights[len(got.Nights)-4].InBedMilli == nil {
		t.Error("the day before the gap lost its metrics")
	}
}

func TestBuildSleep_EmptyWindowIsAllNullNights(t *testing.T) {
	got := buildSleep(nil, testNow, time.UTC)
	if got == nil {
		t.Fatal("buildSleep = nil, want a section for a connected user with no data")
	}
	if len(got.Nights) != sleepWindowDays {
		t.Fatalf("len(Nights) = %d, want %d", len(got.Nights), sleepWindowDays)
	}
	for i, n := range got.Nights {
		if n.Date == "" {
			t.Fatalf("Nights[%d] has no date", i)
		}
		if n.InBedMilli != nil {
			t.Errorf("Nights[%d] carries metrics, want null", i)
		}
	}
	if got.LastNight != nil {
		t.Errorf("LastNight = %+v, want nil", got.LastNight)
	}
}

func TestBuildSleep_LastNightIsTodaysNonNapRecord(t *testing.T) {
	loc := time.UTC
	short := night(dayBefore(0), "fragment", 3600000, dayBefore(0)+"T08:00:00Z")
	long := night(dayBefore(0), "main", 25200000, dayBefore(0)+"T06:30:00Z")
	long.PerformancePct = rhrPtr(91)

	got := buildSleep([]whoopsleep.Entry{short, long}, testNow, loc)
	if got.LastNight == nil {
		t.Fatal("LastNight = nil, want today's main sleep")
	}
	if got.LastNight.Date != dayBefore(0) {
		t.Errorf("LastNight.Date = %q, want today", got.LastNight.Date)
	}
	if got.LastNight.InBedMilli == nil || *got.LastNight.InBedMilli != 25200000 {
		t.Errorf("LastNight in_bed_milli = %v, want the longest record's 25200000", got.LastNight.InBedMilli)
	}
	if got.LastNight.PerformancePct == nil || *got.LastNight.PerformancePct != 91 {
		t.Errorf("LastNight performance_pct = %v, want 91", got.LastNight.PerformancePct)
	}
}

// A nap today is not "last night": promoting anything other than today's non-nap
// record into that slot is the bug the recovery tile's no-reading branch exists
// to avoid.
func TestBuildSleep_LastNightNilWhenTodayHasOnlyANap(t *testing.T) {
	nap := night(dayBefore(0), "nap", 3600000, dayBefore(0)+"T14:00:00Z")
	nap.IsNap = true
	yesterday := night(dayBefore(1), "yday", 28800000, dayBefore(1)+"T07:00:00Z")

	got := buildSleep([]whoopsleep.Entry{nap, yesterday}, testNow, time.UTC)
	if got.LastNight != nil {
		t.Errorf("LastNight = %+v, want nil (today has only a nap)", got.LastNight)
	}
	// Today's slot in the window is still present, carrying null metrics.
	last := got.Nights[len(got.Nights)-1]
	if last.Date != dayBefore(0) {
		t.Fatalf("last night date = %q, want today", last.Date)
	}
	if last.InBedMilli != nil {
		t.Errorf("today's night should be null-metric, got %+v", last)
	}
	// Yesterday still resolves.
	if got.Nights[len(got.Nights)-2].InBedMilli == nil {
		t.Error("yesterday's night is missing its metrics")
	}
}

// buildSleep is pure: entries outside the window never leak into it, and rows
// from a different local date never bleed across.
func TestBuildSleep_IgnoresRecordsOutsideTheWindow(t *testing.T) {
	old := night(dayBefore(sleepWindowDays), "too-old", 28800000, dayBefore(sleepWindowDays)+"T07:00:00Z")
	got := buildSleep([]whoopsleep.Entry{old}, testNow, time.UTC)
	for i, n := range got.Nights {
		if n.InBedMilli != nil {
			t.Fatalf("Nights[%d] (%s) picked up an out-of-window record", i, n.Date)
		}
	}
}
