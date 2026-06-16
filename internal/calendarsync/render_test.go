package calendarsync

import (
	"strings"
	"testing"
	"time"

	plannedworkout "github.com/jwallace145/progressive-overload-fitness-tracker/internal/planned_workout"
)

func strPtr(s string) *string                                                  { return &s }
func intPtr(i int) *int                                                        { return &i }
func f64Ptr(f float64) *float64                                                { return &f }
func detailPtr(d plannedworkout.CalendarDetail) *plannedworkout.CalendarDetail { return &d }

func samplePlan() *plannedworkout.PlannedWorkout {
	return &plannedworkout.PlannedWorkout{
		ID:                "plan-1",
		UserID:            "user-1",
		Name:              strPtr("Push Day"),
		ScheduledStartUTC: time.Date(2026, 6, 20, 17, 0, 0, 0, time.UTC),
		ScheduledEndUTC:   time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC),
		Timezone:          "America/New_York",
		Exercises: []plannedworkout.PlannedExercise{
			{
				ExerciseID: "Bench Press",
				Sets: []plannedworkout.PlannedSet{
					{TargetReps: intPtr(5), TargetWeight: f64Ptr(135), Unit: strPtr("lb"), TargetRPE: f64Ptr(8)},
					{TargetReps: intPtr(5)}, // only reps; weight/RPE nil → omitted
				},
			},
		},
	}
}

func TestRenderEvent_SummaryWindowAndAgenda(t *testing.T) {
	plan := samplePlan()
	ev := RenderEvent(plan, DetailTimeBlock, "https://app.example.com")

	if ev.Summary != "Push Day" {
		t.Errorf("summary = %q, want Push Day", ev.Summary)
	}
	if ev.Timezone != "America/New_York" {
		t.Errorf("timezone = %q", ev.Timezone)
	}
	if !ev.StartUTC.Equal(plan.ScheduledStartUTC) || !ev.EndUTC.Equal(plan.ScheduledEndUTC) {
		t.Errorf("window not carried through: %v / %v", ev.StartUTC, ev.EndUTC)
	}
	// The agenda is always listed now — detail level no longer gates it.
	if !strings.Contains(ev.Description, "Bench Press") {
		t.Errorf("description should include the agenda, got: %q", ev.Description)
	}
	if !strings.Contains(ev.Description, "app.example.com/planned-workouts/plan-1") {
		t.Errorf("expected app link, got: %q", ev.Description)
	}
}

func TestRenderEvent_IncludesNotes(t *testing.T) {
	plan := samplePlan()
	plan.Notes = strPtr("Hit a PR last week — push for 140")
	ev := RenderEvent(plan, DetailTimeBlock, "")

	if !strings.Contains(ev.Description, "Hit a PR last week") {
		t.Errorf("description should include the plan's notes, got: %q", ev.Description)
	}
	// Notes lead, agenda follows.
	if strings.Index(ev.Description, "Hit a PR") > strings.Index(ev.Description, "Bench Press") {
		t.Errorf("notes should precede the agenda, got: %q", ev.Description)
	}
}

func TestRenderEvent_EmptyFallback(t *testing.T) {
	// A lift plan with no notes and no exercises → generic reserved-slot copy.
	plan := samplePlan()
	plan.Notes = nil
	plan.Exercises = nil
	ev := RenderEvent(plan, DetailTimeBlock, "")
	if !strings.Contains(ev.Description, "Reserved") {
		t.Errorf("expected reserved-slot fallback, got: %q", ev.Description)
	}
}

func TestRenderEvent_NoLinkBase(t *testing.T) {
	ev := RenderEvent(samplePlan(), DetailTimeBlock, "")
	if strings.Contains(ev.Description, "Open in Prog Strength") {
		t.Errorf("no link expected when base unset, got: %q", ev.Description)
	}
}

func TestRenderEvent_FullAgenda(t *testing.T) {
	ev := RenderEvent(samplePlan(), DetailFullAgenda, "")

	if !strings.Contains(ev.Description, "Bench Press") {
		t.Errorf("expected exercise line, got: %q", ev.Description)
	}
	// First set has all targets.
	if !strings.Contains(ev.Description, "5 reps") || !strings.Contains(ev.Description, "135 lb") || !strings.Contains(ev.Description, "RPE 8") {
		t.Errorf("expected full set targets, got: %q", ev.Description)
	}
	// Second set is reps-only; weight/RPE must be omitted (no stray "lb"/"RPE"
	// belonging to that set is asserted by counting RPE occurrences == 1).
	if got := strings.Count(ev.Description, "RPE"); got != 1 {
		t.Errorf("RPE appeared %d times, want 1 (nil RPE omitted), desc: %q", got, ev.Description)
	}
}

func TestRenderEvent_Superset(t *testing.T) {
	// Two exercises sharing a superset group bracket under a "Superset:"
	// header; a trailing standalone exercise renders on its own.
	g := 1
	plan := &plannedworkout.PlannedWorkout{
		ID:                "plan-ss",
		ScheduledStartUTC: time.Date(2026, 6, 20, 17, 0, 0, 0, time.UTC),
		ScheduledEndUTC:   time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC),
		Timezone:          "UTC",
		Exercises: []plannedworkout.PlannedExercise{
			{ExerciseID: "Bench Press", SupersetGroup: &g, Sets: []plannedworkout.PlannedSet{{TargetReps: intPtr(5)}}},
			{ExerciseID: "Barbell Row", SupersetGroup: &g, Sets: []plannedworkout.PlannedSet{{TargetReps: intPtr(8)}}},
			{ExerciseID: "Plank", Sets: []plannedworkout.PlannedSet{{TargetReps: intPtr(1)}}},
		},
	}
	ev := RenderEvent(plan, DetailFullAgenda, "")

	if !strings.Contains(ev.Description, "Superset:") {
		t.Errorf("expected a Superset header, got: %q", ev.Description)
	}
	if !strings.Contains(ev.Description, "Bench Press") || !strings.Contains(ev.Description, "Barbell Row") {
		t.Errorf("expected both superset members, got: %q", ev.Description)
	}
	// The standalone exercise is outside the bracket — its line is not indented
	// to the superset's deeper level.
	if !strings.Contains(ev.Description, "\nPlank") {
		t.Errorf("expected standalone exercise on its own line, got: %q", ev.Description)
	}
}

func TestRenderEvent_UnnamedPlan(t *testing.T) {
	plan := samplePlan()
	plan.Name = nil
	ev := RenderEvent(plan, DetailTimeBlock, "")
	if ev.Summary != defaultSummary {
		t.Errorf("summary = %q, want %q", ev.Summary, defaultSummary)
	}
}

func sampleRunPlan() *plannedworkout.PlannedWorkout {
	rt := plannedworkout.RunTypeIntervals
	return &plannedworkout.PlannedWorkout{
		ID:                "run-1",
		UserID:            "user-1",
		ActivityKind:      plannedworkout.ActivityKindRun,
		ScheduledStartUTC: time.Date(2026, 6, 20, 6, 0, 0, 0, time.UTC),
		ScheduledEndUTC:   time.Date(2026, 6, 20, 7, 0, 0, 0, time.UTC),
		Timezone:          "America/New_York",
		RunType:           &rt,
		RunDetails:        strPtr("4x800m @ 5k pace, 90s jog recovery"),
	}
}

func TestRenderEvent_RunFullAgenda(t *testing.T) {
	ev := RenderEvent(sampleRunPlan(), DetailFullAgenda, "")

	if !strings.Contains(ev.Description, "Interval run") {
		t.Errorf("expected run-type heading, got: %q", ev.Description)
	}
	if !strings.Contains(ev.Description, "4x800m @ 5k pace") {
		t.Errorf("expected run details, got: %q", ev.Description)
	}
	// A run must never render an exercise agenda.
	if strings.Contains(ev.Description, "Bench Press") {
		t.Errorf("run rendered a lift agenda, got: %q", ev.Description)
	}
}

func TestRenderEvent_RunAlwaysIncludesDetails(t *testing.T) {
	// Run type + details are always included now (detail level no longer gates).
	ev := RenderEvent(sampleRunPlan(), DetailTimeBlock, "")
	if !strings.Contains(ev.Description, "Interval run") {
		t.Errorf("expected run-type heading, got: %q", ev.Description)
	}
	if !strings.Contains(ev.Description, "4x800m") {
		t.Errorf("expected run details, got: %q", ev.Description)
	}
}

func TestSummaryFor_UnnamedDefaultsToKind(t *testing.T) {
	run := sampleRunPlan() // unnamed run
	if ev := RenderEvent(run, DetailTimeBlock, ""); ev.Summary != "Run" {
		t.Errorf("unnamed run summary = %q, want Run", ev.Summary)
	}

	lift := samplePlan()
	lift.Name = nil
	lift.ActivityKind = plannedworkout.ActivityKindLift
	if ev := RenderEvent(lift, DetailTimeBlock, ""); ev.Summary != "Lift" {
		t.Errorf("unnamed lift summary = %q, want Lift", ev.Summary)
	}
}

func TestRenderCompletedEvent(t *testing.T) {
	ev := RenderCompletedEvent(samplePlan(), "Bench Press 3x5 @ 140 lb", DetailFullAgenda, "")
	if !strings.HasPrefix(ev.Summary, "✓ Completed") {
		t.Errorf("completed summary should be marked, got: %q", ev.Summary)
	}
	if !strings.Contains(ev.Description, "140 lb") {
		t.Errorf("expected actual text in description, got: %q", ev.Description)
	}
}

func TestEffectiveDetail_Precedence(t *testing.T) {
	planFull := samplePlan()
	planFull.CalendarDetail = detailPtr(DetailFullAgenda)

	// override beats everything.
	if got := EffectiveDetail(planFull, "time_block", "full_agenda"); got != DetailTimeBlock {
		t.Errorf("override: got %q want time_block", got)
	}
	// plan beats user default when no override.
	if got := EffectiveDetail(planFull, "", "time_block"); got != DetailFullAgenda {
		t.Errorf("plan: got %q want full_agenda", got)
	}
	// user default applies when override + plan absent.
	planNoDetail := samplePlan()
	planNoDetail.CalendarDetail = nil
	if got := EffectiveDetail(planNoDetail, "", "full_agenda"); got != DetailFullAgenda {
		t.Errorf("user default: got %q want full_agenda", got)
	}
	// nothing set → time_block fallback.
	if got := EffectiveDetail(planNoDetail, "", ""); got != DetailTimeBlock {
		t.Errorf("fallback: got %q want time_block", got)
	}
	// unknown override is ignored, falls through to plan.
	if got := EffectiveDetail(planFull, "garbage", ""); got != DetailFullAgenda {
		t.Errorf("bad override: got %q want full_agenda (plan)", got)
	}
}
