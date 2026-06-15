package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// timelineHydrator renders timeline post content from the live workout and
// activity source tables at read time. It implements timeline.SourceHydrator
// and lives in the wiring layer so the timeline domain never imports
// workout/activity internals (the SOW's clean-boundary requirement).
//
// Hydrate groups a feed page's refs by source_type and does the work per
// type, batching where a batch read exists (PR events) and per-id fetching
// only where no batch read is available (workouts, activities) — never an
// N+1 across types. Refs whose source no longer exists are omitted from the
// returned map; the handler renders that as a dropped post.
type timelineHydrator struct {
	workoutRepo  workout.Repository
	activityRepo activity.Repository
}

// newTimelineHydrator builds the adapter over the workout + activity repos.
func newTimelineHydrator(workoutRepo workout.Repository, activityRepo activity.Repository) *timelineHydrator {
	return &timelineHydrator{workoutRepo: workoutRepo, activityRepo: activityRepo}
}

var _ timeline.SourceHydrator = (*timelineHydrator)(nil)

// Hydrate renders content for a page of posts, grouped by source_type.
func (h *timelineHydrator) Hydrate(ctx context.Context, refs []timeline.PostRef) (map[timeline.PostRef]timeline.PostContent, error) {
	out := make(map[timeline.PostRef]timeline.PostContent, len(refs))

	// Group by source_type so each type's fetch strategy runs once over its
	// slice rather than re-dispatching per ref.
	byType := make(map[timeline.SourceType][]timeline.PostRef)
	for _, ref := range refs {
		byType[ref.SourceType] = append(byType[ref.SourceType], ref)
	}

	if err := h.hydrateWorkouts(ctx, byType[timeline.SourceWorkout], out); err != nil {
		return nil, err
	}
	if err := h.hydrateRuns(ctx, byType[timeline.SourceRun], out); err != nil {
		return nil, err
	}
	if err := h.hydratePRs(ctx, byType[timeline.SourcePR], out); err != nil {
		return nil, err
	}
	if err := h.hydrateBestEfforts(ctx, byType[timeline.SourceBestEffort], out); err != nil {
		return nil, err
	}

	return out, nil
}

// hydrateWorkouts renders `workout` posts. No batch read exists on the
// workout repo, so we fetch per id (GetByID); a missing/deleted workout is
// omitted. Href points at the workouts list view — there is no standalone
// workout-detail web route, the Activities page hosts the workouts tab.
func (h *timelineHydrator) hydrateWorkouts(ctx context.Context, refs []timeline.PostRef, out map[timeline.PostRef]timeline.PostContent) error {
	for _, ref := range refs {
		w, err := h.workoutRepo.GetByID(ctx, ref.SourceID)
		if err != nil {
			// Source gone (deleted/not found): omit from the page.
			continue
		}
		title := w.Name
		if strings.TrimSpace(title) == "" {
			title = "Workout"
		}

		exerciseCount := len(w.Exercises)
		totalSets := 0
		var totalVolume float64
		for _, ex := range w.Exercises {
			totalSets += len(ex.Sets)
			for _, s := range ex.Sets {
				totalVolume += s.Weight * float64(s.Reps)
			}
		}

		metrics := []string{pluralCount(exerciseCount, "exercise")}
		if totalSets > 0 {
			metrics = append(metrics, pluralCount(totalSets, "set"))
		}
		if totalVolume > 0 {
			metrics = append(metrics, fmt.Sprintf("%s lb", formatThousands(totalVolume)))
		}

		out[ref] = timeline.PostContent{
			Title:    title,
			Subtitle: pluralCount(exerciseCount, "exercise"),
			Metrics:  metrics,
			// /activities?view=workouts — the workouts tab of the consolidated
			// Activities page; there's no per-workout detail route in web v1.
			Href: "/activities?view=workouts",
		}
	}
	return nil
}

// hydrateRuns renders `run` posts. No batch read exists, so fetch per id via
// Get(ctx, userID, id) (refs carry the author's UserID). A missing/deleted
// activity is omitted. Href points at the running view of the Activities page.
func (h *timelineHydrator) hydrateRuns(ctx context.Context, refs []timeline.PostRef, out map[timeline.PostRef]timeline.PostContent) error {
	for _, ref := range refs {
		a, err := h.activityRepo.Get(ctx, ref.UserID, ref.SourceID)
		if err != nil {
			continue
		}
		title := "Run"
		if a.Name != nil && strings.TrimSpace(*a.Name) != "" {
			title = *a.Name
		}

		distance := formatMiles(a.DistanceMeters)
		duration := formatDuration(float64(a.DurationSeconds))
		metrics := []string{distance, duration}

		out[ref] = timeline.PostContent{
			Title:    title,
			Subtitle: distance + " · " + duration,
			Metrics:  metrics,
			// /activities?view=running — the running tab of the Activities page.
			Href: "/activities?view=running",
		}
	}
	return nil
}

// hydratePRs renders `pr` posts. The workout repo exposes a batch read keyed
// by event id, so this is a single query for the whole page's PR refs (no
// N+1). A PR event that no longer exists is omitted. Href points at the
// Personal Records page.
func (h *timelineHydrator) hydratePRs(ctx context.Context, refs []timeline.PostRef, out map[timeline.PostRef]timeline.PostContent) error {
	if len(refs) == 0 {
		return nil
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.SourceID
	}
	events, err := h.workoutRepo.GetPersonalRecordEventsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]workout.PersonalRecordEvent, len(events))
	for _, e := range events {
		byID[e.ID] = e
	}
	for _, ref := range refs {
		e, ok := byID[ref.SourceID]
		if !ok {
			continue
		}
		out[ref] = timeline.PostContent{
			Title:    fmt.Sprintf("%s PR", e.ExerciseID),
			Subtitle: "New personal record",
			Metrics:  []string{fmt.Sprintf("%s %s × %d", formatWeight(e.Weight), e.Unit, e.Reps)},
			// /personal-records — the Personal Records page.
			Href: "/personal-records",
		}
	}
	return nil
}

// hydrateBestEfforts renders `best_effort` posts. The source_id is
// "<activityID>:<distanceKey>"; we split on the last ':' (activity ids never
// contain one, but splitting on the last keeps it robust), fetch the activity
// per id, and find the matching best effort by distance_key. A gone activity
// or distance is omitted. Href points at the running view of the Activities page.
func (h *timelineHydrator) hydrateBestEfforts(ctx context.Context, refs []timeline.PostRef, out map[timeline.PostRef]timeline.PostContent) error {
	for _, ref := range refs {
		activityID, distanceKey, ok := splitBestEffortSourceID(ref.SourceID)
		if !ok {
			continue
		}
		a, err := h.activityRepo.Get(ctx, ref.UserID, activityID)
		if err != nil {
			continue
		}
		var matched *activity.ActivityBestEffort
		for i := range a.BestEfforts {
			if a.BestEfforts[i].DistanceKey == distanceKey {
				matched = &a.BestEfforts[i]
				break
			}
		}
		if matched == nil {
			// Distance no longer present on the activity: omit.
			continue
		}
		label := distanceLabel(distanceKey)
		out[ref] = timeline.PostContent{
			Title:    fmt.Sprintf("%s best effort", label),
			Subtitle: "Running best effort",
			Metrics:  []string{formatDuration(matched.DurationSeconds)},
			// /activities?view=running — the running tab of the Activities page.
			Href: "/activities?view=running",
		}
	}
	return nil
}

// --- formatting helpers (small + local) --------------------------------

// splitBestEffortSourceID splits a "<activityID>:<distanceKey>" composite id
// on the last ':'. Returns ok=false when there's no ':' (malformed id).
func splitBestEffortSourceID(s string) (activityID, distanceKey string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// distanceLabel maps a standard distance key to its display label, falling
// back to the raw key for an unknown one.
func distanceLabel(key string) string {
	for _, d := range activity.StandardDistances {
		if d.Key == key {
			return d.DisplayName
		}
	}
	return key
}

// metersPerMile converts the activity package's metric-internal distances to
// the miles the cards render. Display-only; the API stays metric.
const metersPerMile = 1609.344

// formatMiles renders meters as a one-decimal mile string, e.g. "5.0 mi".
func formatMiles(meters float64) string {
	return fmt.Sprintf("%.1f mi", meters/metersPerMile)
}

// formatDuration renders seconds as "M:SS" (or "H:MM:SS" past an hour), e.g.
// 2472s → "41:12", matching the SOW's example chips.
func formatDuration(seconds float64) string {
	total := int(seconds + 0.5)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// formatWeight renders a weight without a trailing ".0" for whole numbers,
// e.g. 305.0 → "305", 102.5 → "102.5".
func formatWeight(w float64) string {
	if w == float64(int64(w)) {
		return fmt.Sprintf("%d", int64(w))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", w), "0"), ".")
}

// formatThousands renders a whole-number volume with thousands separators,
// e.g. 8400 → "8,400".
func formatThousands(v float64) string {
	n := int64(v + 0.5)
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}

// pluralCount renders "{n} {noun}" with a naive plural 's' for n != 1, e.g.
// 1 → "1 exercise", 3 → "3 exercises".
func pluralCount(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
