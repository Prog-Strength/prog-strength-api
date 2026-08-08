package strength

import (
	"fmt"
	"strings"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/exercise"
)

// exerciseNameByID resolves a catalog exercise id ("barbell-bench-press") to
// its display name ("Barbell Bench Press"). Built once from the canonical
// catalog, like the planned-workout renderer's equivalent map.
var exerciseNameByID = func() map[string]string {
	m := make(map[string]string, len(exercise.Catalog))
	for _, e := range exercise.Catalog {
		m[e.ID] = e.Name
	}
	return m
}()

// calendarEvent renders a logged lift's calendar manifestation. The headline
// reuses the card's exercises/sets/volume chips verbatim — a calendar that
// reported a different total volume than the workout card would be a bug, so
// both read from the same tally — and the body carries the thing a card has
// no room for: what was actually lifted.
func calendarEvent(a activity.Activity, details any) activity.CalendarManifest {
	var exercises []WorkoutExercise
	if d, ok := details.(*Details); ok && d != nil {
		exercises = d.Exercises
	}

	title := "Workout"
	if a.Name != nil && strings.TrimSpace(*a.Name) != "" {
		title = *a.Name
	}

	m := activity.CalendarManifest{
		Title:    title,
		Headline: strings.Join(summarize(a, details).Metrics, " · "),
	}
	if agenda := renderLoggedAgenda(exercises); len(agenda) > 0 {
		m.Sections = append(m.Sections, activity.ManifestSection{Lines: agenda})
	}
	if a.Notes != nil {
		if notes := strings.TrimSpace(*a.Notes); notes != "" {
			m.Sections = append(m.Sections, activity.ManifestSection{
				Heading: "Notes",
				Lines:   strings.Split(notes, "\n"),
			})
		}
	}
	return m
}

// renderLoggedAgenda renders the session's exercises as numbered entries with
// collapsed set groups, e.g.:
//
//  1. Barbell Bench Press
//     • 3 sets × 8 reps @ 135 lb
//     • 1 set × 6 reps @ 155 lb
//
//     Superset
//
//  2. Incline Dumbbell Bench Press
//     • 3 sets × 10 reps @ 50 lb
//
//  3. Dumbbell Tripod Row
//     • 3 sets × 10 reps @ 60 lb
//
// It mirrors the planned-lift agenda in calendarsync/render.go so a planned
// session and the session that fulfilled it read the same way in the same
// event — the takeover patches one into the other, and a formatting mismatch
// there would look like corruption rather than progress.
func renderLoggedAgenda(exercises []WorkoutExercise) []string {
	var lines []string
	num := 0
	for gi, group := range groupBySuperset(exercises) {
		if gi > 0 {
			lines = append(lines, "")
		}
		indent := ""
		if len(group) > 1 {
			lines = append(lines, "Superset")
			indent = "  "
		}
		for _, ex := range group {
			num++
			lines = append(lines, fmt.Sprintf("%s%d. %s", indent, num, exerciseLabel(ex)))
			for _, g := range groupSets(ex.Sets) {
				lines = append(lines, indent+"   • "+renderSetGroup(g))
			}
		}
	}
	return lines
}

// setGroup is a run of identical consecutive sets collapsed into a count.
type setGroup struct {
	count int
	set   Set
}

// groupSets collapses consecutive identical sets (same reps/weight/unit) into
// counted groups, so 8,8,8 renders as "3 sets × 8 reps" rather than three
// near-identical lines. Only CONSECUTIVE sets merge: a 8,8,6,8 progression
// keeps its shape, because the order a lifter worked through the weight is
// itself information.
func groupSets(sets []Set) []setGroup {
	var out []setGroup
	for _, s := range sets {
		if n := len(out); n > 0 && out[n-1].set == s {
			out[n-1].count++
			continue
		}
		out = append(out, setGroup{count: 1, set: s})
	}
	return out
}

// renderSetGroup renders one collapsed group, e.g. "3 sets × 8 reps @ 135 lb".
// A zero weight drops the weight clause entirely, since that is how bodyweight
// work is stored (Set.Weight == 0) and "@ 0 lb" would be wrong, not just ugly.
func renderSetGroup(g setGroup) string {
	setWord := "sets"
	if g.count == 1 {
		setWord = "set"
	}
	line := fmt.Sprintf("%d %s × %s", g.count, setWord, activity.PluralCount(g.set.Reps, "rep"))
	if g.set.Weight > 0 {
		line += fmt.Sprintf(" @ %s %s", trimFloat(g.set.Weight), g.set.Unit)
	}
	return line
}

// groupBySuperset buckets CONSECUTIVE exercises sharing the same non-nil
// superset group. Order is the source of truth, so non-adjacent exercises
// carrying the same group id are deliberately not merged — matching both the
// logged-workout grouping and the planned-agenda renderer.
func groupBySuperset(exercises []WorkoutExercise) [][]WorkoutExercise {
	var groups [][]WorkoutExercise
	for _, ex := range exercises {
		if n := len(groups); n > 0 {
			prev := groups[n-1][len(groups[n-1])-1]
			if ex.SupersetGroup != nil && prev.SupersetGroup != nil && *ex.SupersetGroup == *prev.SupersetGroup {
				groups[n-1] = append(groups[n-1], ex)
				continue
			}
		}
		groups = append(groups, []WorkoutExercise{ex})
	}
	return groups
}

// exerciseLabel is the catalog display name, falling back to the raw id when
// the id is not in the catalog (a custom or retired exercise).
func exerciseLabel(ex WorkoutExercise) string {
	if name, ok := exerciseNameByID[ex.ExerciseID]; ok {
		return name
	}
	return ex.ExerciseID
}

// trimFloat formats a weight without a trailing ".0", so 135.0 renders as
// "135" while 137.5 stays "137.5".
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
