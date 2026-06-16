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

// summaryFor returns the event summary for a plan: its name, or — when
// unnamed — a label derived from the activity kind ("Run" / "Lift") so two
// same-day events read distinctly on the calendar. Falls back to the generic
// default for an unknown kind.
func summaryFor(plan *plannedworkout.PlannedWorkout) string {
	if plan != nil && plan.Name != nil && strings.TrimSpace(*plan.Name) != "" {
		return *plan.Name
	}
	if plan != nil {
		switch plan.ActivityKind {
		case plannedworkout.ActivityKindRun:
			return "Run"
		case plannedworkout.ActivityKindLift:
			return "Lift"
		}
	}
	return defaultSummary
}

// RenderEvent renders the Google Calendar event body for a plan. The
// description always carries the user's notes (when set) followed by the
// activity-appropriate agenda — a lift's exercises/sets, or a run's type +
// details — so the synced event always reflects what the session is. The
// window + timezone come straight from the plan. appLinkBase is an optional
// base URL linked back to Prog Strength (empty disables the link line).
//
// detail is retained for signature compatibility with the scheduler but no
// longer gates the description: notes + agenda are always included (the
// calendar-detail level is effectively vestigial for rendering).
func RenderEvent(plan *plannedworkout.PlannedWorkout, detail CalendarDetail, appLinkBase string) GoogleEvent {
	_ = detail
	ev := GoogleEvent{
		Summary:  summaryFor(plan),
		StartUTC: plan.ScheduledStartUTC,
		EndUTC:   plan.ScheduledEndUTC,
		Timezone: plan.Timezone,
	}

	// Description sections, top to bottom: notes, then the agenda. A plan with
	// neither falls back to a generic reserved-slot line so the event is never
	// blank.
	var sections []string
	if notes := notesBody(plan); notes != "" {
		sections = append(sections, notes)
	}
	if agenda := agendaBody(plan); agenda != "" {
		sections = append(sections, agenda)
	}
	body := strings.Join(sections, "\n\n")
	if body == "" {
		body = "Reserved training slot."
	}
	if link := planLink(plan, appLinkBase); link != "" {
		body += "\n\n" + link
	}
	ev.Description = body
	return ev
}

// notesBody returns the plan's trimmed free-text notes, or "" when none.
func notesBody(plan *plannedworkout.PlannedWorkout) string {
	if plan != nil && plan.Notes != nil {
		return strings.TrimSpace(*plan.Notes)
	}
	return ""
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

// agendaBody returns the agenda body text for a plan, dispatching on the
// activity kind: a lift renders its exercise agenda, a run renders its run
// type + details. Returns "" when the plan carries no agenda (an empty lift,
// or a run with no type/details) — the caller then falls back to the
// reserved-slot copy.
func agendaBody(plan *plannedworkout.PlannedWorkout) string {
	if plan.ActivityKind == plannedworkout.ActivityKindRun {
		return renderRunAgenda(plan)
	}
	if len(plan.Exercises) > 0 {
		return renderAgenda(plan.Exercises)
	}
	return ""
}

// renderRunAgenda renders a run plan's type + free-text details, e.g.:
//
//	Threshold run
//	4x800m @ 5k pace, 90s jog recovery
//
// Either piece may be absent; returns "" when both are.
func renderRunAgenda(plan *plannedworkout.PlannedWorkout) string {
	var parts []string
	if plan.RunType != nil && *plan.RunType != "" {
		parts = append(parts, runTypeLabel(*plan.RunType))
	}
	if plan.RunDetails != nil && strings.TrimSpace(*plan.RunDetails) != "" {
		parts = append(parts, strings.TrimSpace(*plan.RunDetails))
	}
	return strings.Join(parts, "\n")
}

// runTypeLabel renders a run type as a human heading, e.g. "Threshold run".
// Unknown values fall back to the raw value plus "run".
func runTypeLabel(rt plannedworkout.RunType) string {
	switch rt {
	case plannedworkout.RunTypeEasy:
		return "Easy run"
	case plannedworkout.RunTypeThreshold:
		return "Threshold run"
	case plannedworkout.RunTypeIntervals:
		return "Interval run"
	default:
		return string(rt) + " run"
	}
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
