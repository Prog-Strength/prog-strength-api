package calendarsync

import (
	"fmt"
	"strings"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	plannedworkout "github.com/jwallace145/progressive-overload-fitness-tracker/internal/planned_workout"
)

// divider is the horizontal rule used to frame the branded event body.
const divider = "━━━━━━━━━━━━━━━━━━━━━━━━"

// exerciseNameByID resolves a catalog exercise id (e.g. "barbell-bench-press")
// to its display name ("Barbell Bench Press") for the event body. Built once
// from the canonical catalog.
var exerciseNameByID = func() map[string]string {
	m := make(map[string]string, len(exercise.Catalog))
	for _, e := range exercise.Catalog {
		m[e.ID] = e.Name
	}
	return m
}()

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

	// Branded header: "PROG STRENGTH · Planned Lift/Run" over a divider.
	kind := "Lift"
	if plan.ActivityKind == plannedworkout.ActivityKindRun {
		kind = "Run"
	}
	var b strings.Builder
	b.WriteString("PROG STRENGTH · Planned ")
	b.WriteString(kind)
	b.WriteString("\n")
	b.WriteString(divider)

	// Body sections: notes, then the agenda. A plan with neither falls back to
	// a generic reserved-slot line so the event is never blank.
	hasContent := false
	if notes := notesBody(plan); notes != "" {
		b.WriteString("\n\n")
		b.WriteString(notes)
		hasContent = true
	}
	if agenda := agendaBody(plan); agenda != "" {
		b.WriteString("\n\n")
		b.WriteString(agenda)
		hasContent = true
	}
	if !hasContent {
		b.WriteString("\n\nReserved training slot.")
	}

	// Footer: divider + the "open in Prog Strength" link, when configured.
	if link := planLink(plan, appLinkBase); link != "" {
		b.WriteString("\n\n")
		b.WriteString(divider)
		b.WriteString("\n")
		b.WriteString(link)
	}

	ev.Description = b.String()
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

// renderAgenda renders a numbered lift agenda with collapsed sets, e.g.:
//
//  1. Barbell Bench Press
//     • 1 set × 10 reps
//     • 3 sets × 8 reps @ 135 lb
//
//  2. Barbell Bent Over Row
//     • 3 sets × 8 reps
//
// Exercises are numbered sequentially across the whole agenda. Consecutive
// exercises sharing a superset group are bracketed under a "Superset" header
// and indented, with numbering continuing through them:
//
//	Superset
//	  3. Incline Dumbbell Bench Press
//	     • 3 sets × 8 reps
//	     • 1 set × AMRAP
//	  4. Dumbbell Tripod Row
//	     • 4 sets × 8 reps
func renderAgenda(exercises []plannedworkout.PlannedExercise) string {
	var b strings.Builder
	num := 0
	for gi, group := range groupBySuperset(exercises) {
		if gi > 0 {
			b.WriteString("\n\n")
		}
		if len(group) > 1 {
			b.WriteString("Superset")
			for _, ex := range group {
				num++
				b.WriteString("\n")
				b.WriteString(renderNumberedExercise(num, ex, "  "))
			}
		} else {
			num++
			b.WriteString(renderNumberedExercise(num, group[0], ""))
		}
	}
	return b.String()
}

// renderNumberedExercise renders "{indent}{n}. {Name}" plus one bullet line per
// collapsed set group, each indented under the name.
func renderNumberedExercise(num int, ex plannedworkout.PlannedExercise, indent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%d. %s", indent, num, exerciseLabel(ex))
	for _, g := range groupSets(ex.Sets) {
		b.WriteString("\n")
		b.WriteString(indent)
		b.WriteString("   • ")
		b.WriteString(renderSetGroup(g))
	}
	return b.String()
}

// setGroup is a run of identical consecutive sets collapsed into a count.
type setGroup struct {
	count int
	set   plannedworkout.PlannedSet
}

// groupSets collapses consecutive identical sets (same reps/weight/unit/RPE
// and AMRAP flag) into counted groups, so "8,8,8" renders as "3 sets × 8 reps".
func groupSets(sets []plannedworkout.PlannedSet) []setGroup {
	var out []setGroup
	for _, s := range sets {
		if n := len(out); n > 0 && setsEqual(out[n-1].set, s) {
			out[n-1].count++
		} else {
			out = append(out, setGroup{count: 1, set: s})
		}
	}
	return out
}

// renderSetGroup renders one collapsed set group, e.g. "3 sets × 8 reps @ 135
// lb · RPE 8", or "1 set × AMRAP" for an AMRAP target.
func renderSetGroup(g setGroup) string {
	setWord := "sets"
	if g.count == 1 {
		setWord = "set"
	}
	var reps string
	switch {
	case g.set.AMRAP:
		reps = "AMRAP"
	case g.set.TargetReps != nil:
		reps = fmt.Sprintf("%d reps", *g.set.TargetReps)
	default:
		reps = "—"
	}
	line := fmt.Sprintf("%d %s × %s", g.count, setWord, reps)
	if g.set.TargetWeight != nil {
		unit := ""
		if g.set.Unit != nil {
			unit = " " + *g.set.Unit
		}
		line += fmt.Sprintf(" @ %s%s", trimFloat(*g.set.TargetWeight), unit)
	}
	if g.set.TargetRPE != nil {
		line += fmt.Sprintf(" · RPE %s", trimFloat(*g.set.TargetRPE))
	}
	return line
}

func setsEqual(a, b plannedworkout.PlannedSet) bool {
	return eqIntPtr(a.TargetReps, b.TargetReps) &&
		eqFloatPtr(a.TargetWeight, b.TargetWeight) &&
		eqStrPtr(a.Unit, b.Unit) &&
		eqFloatPtr(a.TargetRPE, b.TargetRPE) &&
		a.AMRAP == b.AMRAP
}

func eqIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// groupBySuperset buckets consecutive exercises that share the same non-nil
// superset group into one slice; standalone exercises (nil group, or a group
// not matching the previous exercise) start their own single-element bucket.
// Mirrors the logged-workout grouping — order is the source of truth, so
// non-adjacent same-group exercises are intentionally not merged.
func groupBySuperset(exercises []plannedworkout.PlannedExercise) [][]plannedworkout.PlannedExercise {
	var groups [][]plannedworkout.PlannedExercise
	for _, ex := range exercises {
		if n := len(groups); n > 0 {
			prev := groups[n-1][len(groups[n-1])-1]
			if ex.SupersetGroup != nil && prev.SupersetGroup != nil && *ex.SupersetGroup == *prev.SupersetGroup {
				groups[n-1] = append(groups[n-1], ex)
				continue
			}
		}
		groups = append(groups, []plannedworkout.PlannedExercise{ex})
	}
	return groups
}

// exerciseLabel is the heading for an exercise: its catalog display name,
// falling back to the raw id when the id isn't in the catalog.
func exerciseLabel(ex plannedworkout.PlannedExercise) string {
	if name, ok := exerciseNameByID[ex.ExerciseID]; ok {
		return name
	}
	return ex.ExerciseID
}

// trimFloat formats a float without a trailing ".0" so 135.0 renders as "135"
// while 137.5 stays "137.5".
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// planLink renders the footer link to the plan in Prog Strength, or "" when no
// base URL is configured.
func planLink(plan *plannedworkout.PlannedWorkout, appLinkBase string) string {
	base := strings.TrimRight(appLinkBase, "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("↗ Open in Prog Strength\n%s/planned-workouts/%s", base, plan.ID)
}
