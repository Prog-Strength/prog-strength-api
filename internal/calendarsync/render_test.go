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

func TestRenderEvent_TimeBlock(t *testing.T) {
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
	// time_block must NOT list the agenda.
	if strings.Contains(ev.Description, "Bench Press") {
		t.Errorf("time_block description should not include agenda, got: %q", ev.Description)
	}
	if !strings.Contains(ev.Description, "Reserved") {
		t.Errorf("expected reserved-slot note, got: %q", ev.Description)
	}
	if !strings.Contains(ev.Description, "app.example.com/planned-workouts/plan-1") {
		t.Errorf("expected app link, got: %q", ev.Description)
	}
}

func TestRenderEvent_TimeBlockNoLinkBase(t *testing.T) {
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

func TestRenderEvent_UnnamedPlan(t *testing.T) {
	plan := samplePlan()
	plan.Name = nil
	ev := RenderEvent(plan, DetailTimeBlock, "")
	if ev.Summary != defaultSummary {
		t.Errorf("summary = %q, want %q", ev.Summary, defaultSummary)
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
