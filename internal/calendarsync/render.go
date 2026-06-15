package calendarsync

import (
	"fmt"
	"strings"

	plannedworkout "github.com/jwallace145/progressive-overload-fitness-tracker/internal/planned_workout"
)

// CalendarDetail is the calendar event detail level. It mirrors the
// plannedworkout domain's CalendarDetail (time_block / full_agenda) but is
// declared here so the rendering layer doesn't force every caller to import the
// planned_workout package for the constants.
type CalendarDetail = plannedworkout.CalendarDetail

const (
	DetailTimeBlock  = plannedworkout.DetailTimeBlock
	DetailFullAgenda = plannedworkout.DetailFullAgenda
)

// defaultSummary is used when a plan has no name set.
const defaultSummary = "Planned workout"

// EffectiveDetail resolves the detail level to use for a plan, honoring the
// precedence override > plan.CalendarDetail > userDefault, falling back to
// time_block when none resolve to a known value. userDefault is the user's
// CalendarDefaultDetail string (e.g. "time_block"/"full_agenda"); an unknown or
// empty value is ignored.
func EffectiveDetail(plan *plannedworkout.PlannedWorkout, override, userDefault string) CalendarDetail {
	if d, ok := parseDetail(override); ok {
		return d
	}
	if plan != nil && plan.CalendarDetail != nil {
		if d, ok := parseDetail(string(*plan.CalendarDetail)); ok {
			return d
		}
	}
	if d, ok := parseDetail(userDefault); ok {
		return d
	}
	return DetailTimeBlock
}

// parseDetail maps a string to a CalendarDetail, reporting whether it was a
// recognized value.
func parseDetail(s string) (CalendarDetail, bool) {
	switch CalendarDetail(s) {
	case DetailTimeBlock:
		return DetailTimeBlock, true
	case DetailFullAgenda:
		return DetailFullAgenda, true
	default:
		return "", false
	}
}

// summaryFor returns the event summary for a plan: its name, or the default
// when unnamed/blank.
func summaryFor(plan *plannedworkout.PlannedWorkout) string {
	if plan != nil && plan.Name != nil && strings.TrimSpace(*plan.Name) != "" {
		return *plan.Name
	}
	return defaultSummary
}

// RenderEvent renders the Google Calendar event body for a plan at the given
// detail level. appLinkBase is an optional base URL linked back to Prog
// Strength (empty disables the link line). The window + timezone come straight
// from the plan.
func RenderEvent(plan *plannedworkout.PlannedWorkout, detail CalendarDetail, appLinkBase string) GoogleEvent {
	ev := GoogleEvent{
		Summary:  summaryFor(plan),
		StartUTC: plan.ScheduledStartUTC,
		EndUTC:   plan.ScheduledEndUTC,
		Timezone: plan.Timezone,
	}

	var b strings.Builder
	if detail == DetailFullAgenda && len(plan.Exercises) > 0 {
		b.WriteString(renderAgenda(plan.Exercises))
	} else {
		b.WriteString("Reserved training slot.")
	}
	if link := planLink(plan, appLinkBase); link != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(link)
	}
	ev.Description = b.String()
	return ev
}

// RenderCompletedEvent renders the event body for a COMPLETED plan: the summary
// is prefixed with a "✓ Completed" marker and the actual session details
// (actualText, passed in by the Phase 4 completion flow) are noted. detail
// still controls whether the planned agenda is included below the actuals.
func RenderCompletedEvent(plan *plannedworkout.PlannedWorkout, actualText string, detail CalendarDetail, appLinkBase string) GoogleEvent {
	ev := RenderEvent(plan, detail, appLinkBase)
	ev.Summary = "✓ Completed: " + summaryFor(plan)

	var b strings.Builder
	if strings.TrimSpace(actualText) != "" {
		b.WriteString("Completed session:\n")
		b.WriteString(actualText)
	} else {
		b.WriteString("Completed.")
	}
	if ev.Description != "" {
		b.WriteString("\n\n")
		b.WriteString(ev.Description)
	}
	ev.Description = b.String()
	return ev
}

// renderAgenda renders each exercise and its present target sets, one exercise
// per block, e.g.:
//
//	Bench Press
//	  3 × 5 @ RPE 8 (135 lb)
//
// Sets with no target fields at all are still listed by their position so the
// count is visible; nil individual targets are omitted gracefully.
func renderAgenda(exercises []plannedworkout.PlannedExercise) string {
	lines := make([]string, 0, len(exercises))
	for _, ex := range exercises {
		var block strings.Builder
		block.WriteString(exerciseLabel(ex))
		for _, s := range ex.Sets {
			block.WriteString("\n  ")
			block.WriteString(renderSet(s))
		}
		lines = append(lines, block.String())
	}
	return strings.Join(lines, "\n")
}

// exerciseLabel is the heading line for an exercise. The plan model carries the
// exercise id, not a display name, so the id is the stable label here.
func exerciseLabel(ex plannedworkout.PlannedExercise) string {
	return ex.ExerciseID
}

// renderSet renders one target set, omitting nil fields. Shapes:
//
//	"3 × 5"            reps only
//	"5"               weight/RPE present, reps nil → lead with weight/RPE
//	"3 × 5 @ RPE 8 (135 lb)"
func renderSet(s plannedworkout.PlannedSet) string {
	var parts []string

	if s.TargetReps != nil {
		parts = append(parts, fmt.Sprintf("%d reps", *s.TargetReps))
	}
	if s.TargetWeight != nil {
		unit := ""
		if s.Unit != nil {
			unit = " " + *s.Unit
		}
		parts = append(parts, fmt.Sprintf("%s%s", trimFloat(*s.TargetWeight), unit))
	}
	if s.TargetRPE != nil {
		parts = append(parts, fmt.Sprintf("RPE %s", trimFloat(*s.TargetRPE)))
	}
	if len(parts) == 0 {
		return "1 set"
	}
	return strings.Join(parts, " @ ")
}

// trimFloat formats a float without a trailing ".0" so 135.0 renders as "135"
// while 137.5 stays "137.5".
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// planLink renders the "open in Prog Strength" line, or "" when no base URL is
// configured.
func planLink(plan *plannedworkout.PlannedWorkout, appLinkBase string) string {
	base := strings.TrimRight(appLinkBase, "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("Open in Prog Strength: %s/planned-workouts/%s", base, plan.ID)
}
