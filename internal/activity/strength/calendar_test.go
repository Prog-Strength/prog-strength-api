package strength

import (
	"strings"
	"testing"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

func lb(weight float64, reps int) Set {
	return Set{Reps: reps, Weight: weight, Unit: user.WeightUnitPounds}
}

func intPtr(i int) *int { return &i }

// agendaLines returns the rendered agenda section (the one with no heading).
func agendaLines(m activity.CalendarManifest) []string {
	for _, s := range m.Sections {
		if s.Heading == "" {
			return s.Lines
		}
	}
	return nil
}

func TestCalendarEvent_HeadlineMatchesTheCardChips(t *testing.T) {
	a := activity.Activity{ActivityType: activity.ActivityStrengthTraining}
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "barbell-bench-press", Sets: []Set{lb(135, 8), lb(135, 8)}},
	}}

	got := calendarEvent(a, d)
	// The card and the calendar must agree on volume: 135*8*2 = 2,160.
	want := strings.Join(summarize(a, d).Metrics, " · ")
	if got.Headline != want {
		t.Fatalf("Headline = %q, want the card's chips %q", got.Headline, want)
	}
	if !strings.Contains(got.Headline, "2,160 lb") {
		t.Fatalf("Headline = %q, want the total volume", got.Headline)
	}
}

func TestCalendarEvent_CollapsesIdenticalConsecutiveSets(t *testing.T) {
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "barbell-bench-press", Sets: []Set{lb(135, 8), lb(135, 8), lb(135, 8)}},
	}}

	lines := agendaLines(calendarEvent(activity.Activity{}, d))
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want a name line and one collapsed set line", lines)
	}
	if lines[0] != "1. Barbell Bench Press" {
		t.Fatalf("name line = %q", lines[0])
	}
	if lines[1] != "   • 3 sets × 8 reps @ 135 lb" {
		t.Fatalf("set line = %q", lines[1])
	}
}

// A lifter's working order is information: 8,8,6 must not collapse to "3 sets".
func TestCalendarEvent_KeepsNonIdenticalSetsSeparate(t *testing.T) {
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "barbell-bench-press", Sets: []Set{lb(135, 8), lb(135, 8), lb(155, 6)}},
	}}

	lines := agendaLines(calendarEvent(activity.Activity{}, d))
	if len(lines) != 3 {
		t.Fatalf("lines = %v, want a name line and two set lines", lines)
	}
	if lines[1] != "   • 2 sets × 8 reps @ 135 lb" {
		t.Fatalf("first set line = %q", lines[1])
	}
	if lines[2] != "   • 1 set × 6 reps @ 155 lb" {
		t.Fatalf("second set line = %q, want the singular set word", lines[2])
	}
}

// Bodyweight work stores Weight=0; "@ 0 lb" would be wrong, not just ugly.
func TestCalendarEvent_BodyweightSetsOmitTheWeightClause(t *testing.T) {
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "pull-up", Sets: []Set{{Reps: 10, Unit: user.WeightUnitPounds}}},
	}}

	lines := agendaLines(calendarEvent(activity.Activity{}, d))
	if lines[1] != "   • 1 set × 10 reps" {
		t.Fatalf("set line = %q, want no weight clause", lines[1])
	}
}

func TestCalendarEvent_SupersetsAreBracketedAndNumberingContinues(t *testing.T) {
	g := intPtr(1)
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "barbell-bench-press", Sets: []Set{lb(135, 8)}},
		{ExerciseID: "incline-dumbbell-bench-press", SupersetGroup: g, Sets: []Set{lb(50, 10)}},
		{ExerciseID: "dumbbell-tripod-row", SupersetGroup: g, Sets: []Set{lb(60, 10)}},
	}}

	lines := agendaLines(calendarEvent(activity.Activity{}, d))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Superset") {
		t.Fatalf("agenda = %q, want a Superset header", joined)
	}
	// Numbering runs 1,2,3 across the whole agenda, indented inside the group.
	if !strings.Contains(joined, "  2. Incline Dumbbell Bench Press") {
		t.Fatalf("agenda = %q, want the superset members indented and numbered 2", joined)
	}
	if !strings.Contains(joined, "  3. Dumbbell Tripod Row") {
		t.Fatalf("agenda = %q, want numbering to continue to 3", joined)
	}
}

// Non-adjacent exercises sharing a group id must NOT merge — order is the
// source of truth, matching the planned-agenda renderer.
func TestCalendarEvent_NonAdjacentSameGroupDoesNotMerge(t *testing.T) {
	g := intPtr(1)
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "a", SupersetGroup: g, Sets: []Set{lb(10, 1)}},
		{ExerciseID: "b", Sets: []Set{lb(10, 1)}},
		{ExerciseID: "c", SupersetGroup: g, Sets: []Set{lb(10, 1)}},
	}}

	lines := agendaLines(calendarEvent(activity.Activity{}, d))
	if strings.Contains(strings.Join(lines, "\n"), "Superset") {
		t.Fatalf("agenda = %v, want no Superset header for non-adjacent members", lines)
	}
}

func TestCalendarEvent_UnknownExerciseIDFallsBackToTheRawID(t *testing.T) {
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "some-retired-lift", Sets: []Set{lb(100, 5)}},
	}}

	lines := agendaLines(calendarEvent(activity.Activity{}, d))
	if lines[0] != "1. some-retired-lift" {
		t.Fatalf("name line = %q, want the raw id", lines[0])
	}
}

func TestCalendarEvent_TrimsTrailingZeroesOnWeight(t *testing.T) {
	d := &Details{Exercises: []WorkoutExercise{
		{ExerciseID: "a", Sets: []Set{lb(137.5, 5)}},
		{ExerciseID: "b", Sets: []Set{lb(135, 5)}},
	}}

	joined := strings.Join(agendaLines(calendarEvent(activity.Activity{}, d)), "\n")
	if !strings.Contains(joined, "@ 137.5 lb") {
		t.Fatalf("agenda = %q, want 137.5 preserved", joined)
	}
	if !strings.Contains(joined, "@ 135 lb") {
		t.Fatalf("agenda = %q, want 135 without a trailing .0", joined)
	}
}

// A session logged before its exercises are filled in is valid; it must
// render a headline-only event rather than an empty agenda block.
func TestCalendarEvent_EmptySessionRendersNoAgendaSection(t *testing.T) {
	got := calendarEvent(activity.Activity{}, &Details{})
	if agendaLines(got) != nil {
		t.Fatalf("Sections = %+v, want no agenda section", got.Sections)
	}
}

func TestCalendarEvent_NilDetailsDegradeToHeadlineOnly(t *testing.T) {
	got := calendarEvent(activity.Activity{}, nil)
	if got.Title != "Workout" {
		t.Fatalf("Title = %q, want the default", got.Title)
	}
	if agendaLines(got) != nil {
		t.Fatalf("Sections = %+v, want none for nil details", got.Sections)
	}
}
