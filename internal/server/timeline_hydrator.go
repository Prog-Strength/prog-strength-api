package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity/strength"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
)

// timelineHydrator renders timeline post content from the live activity
// source tables at read time. It implements timeline.SourceHydrator and lives
// in the wiring layer so the timeline domain never imports workout/activity
// internals (the SOW's clean-boundary requirement).
//
// Hydrate groups a feed page's refs by source_type and does the work per
// type, batching where a batch read exists (session summaries, PR events) and
// per-id fetching only where no batch read is available (best efforts, which
// need the activity's loaded best-effort list) — never an N+1 across types.
// Refs whose source no longer exists are omitted from the returned map; the
// handler renders that as a dropped post.
type timelineHydrator struct {
	workoutRepo  strength.Repository
	activityRepo activity.Repository
	registry     *activity.Registry
}

// newTimelineHydrator builds the adapter over the workout + activity repos and
// the type registry (whose descriptors' Summarize renders the session cards).
func newTimelineHydrator(workoutRepo strength.Repository, activityRepo activity.Repository, registry *activity.Registry) *timelineHydrator {
	return &timelineHydrator{workoutRepo: workoutRepo, activityRepo: activityRepo, registry: registry}
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

	// `workout` and `run` posts both point at rows in the one activities base
	// table, so they resolve through a single unified path (registry
	// Summarize), not two divergent renderers.
	if err := h.hydrateSessions(ctx, byType[timeline.SourceWorkout], byType[timeline.SourceRun], out); err != nil {
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

// hydrateSessions renders `workout` and `run` posts through the unified
// activity store and the type registry. Both source types point at the
// activities base table, so they share one batched summary read
// (SummariesByIDs, grouped by author since a feed page spans the viewer's
// followees) plus activity.RenderSummaries, which loads each type's detail
// (strength's exercises/sets) in a single batch and renders the same card the
// unified /activities list shows. The card body is byte-identical for both
// types; only the Href differs, and that web-routing concern stays here in the
// wiring layer because Summary deliberately carries no Href. A ref whose
// source no longer resolves is absent from the summaries map and omitted.
func (h *timelineHydrator) hydrateSessions(ctx context.Context, workoutRefs, runRefs []timeline.PostRef, out map[timeline.PostRef]timeline.PostContent) error {
	sessionRefs := make([]timeline.PostRef, 0, len(workoutRefs)+len(runRefs))
	sessionRefs = append(sessionRefs, workoutRefs...)
	sessionRefs = append(sessionRefs, runRefs...)
	if len(sessionRefs) == 0 {
		return nil
	}

	// SummariesByIDs is user-scoped (it enforces ownership), and the feed
	// spans the viewer plus their followees, so batch the base read per author.
	idsByUser := make(map[string][]string)
	for _, ref := range sessionRefs {
		idsByUser[ref.UserID] = append(idsByUser[ref.UserID], ref.SourceID)
	}
	activities := make([]activity.Activity, 0, len(sessionRefs))
	for uid, ids := range idsByUser {
		found, err := h.activityRepo.SummariesByIDs(ctx, uid, ids)
		if err != nil {
			return err
		}
		for _, a := range found {
			activities = append(activities, a)
		}
	}

	// The userID passed to RenderSummaries only feeds each type's batch detail
	// load; strength's is id-scoped (ignores it) and the endurance types
	// summarize off the joined base row, so one render over the merged authors
	// is correct.
	summaries := activity.RenderSummaries(ctx, h.registry, "", activities)
	for _, ref := range sessionRefs {
		s, ok := summaries[ref.SourceID]
		if !ok {
			// Source gone (deleted/not found) or unrenderable: omit.
			continue
		}
		out[ref] = timeline.PostContent{
			Title:    s.Title,
			Subtitle: s.Subtitle,
			Metrics:  s.Metrics,
			Href:     sessionHref(ref.SourceType),
		}
	}
	return nil
}

// sessionHref maps a session post's source type to its web destination — the
// workouts or running tab of the consolidated Activities page. There is no
// per-session detail route in web v1.
func sessionHref(t timeline.SourceType) string {
	if t == timeline.SourceWorkout {
		return "/activities?view=workouts"
	}
	return "/activities?view=running"
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
	byID := make(map[string]strength.PersonalRecordEvent, len(events))
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
// per id (Get loads the best-effort list SummariesByIDs omits), and find the
// matching best effort by distance_key. A gone activity or distance is
// omitted. Href points at the running view of the Activities page.
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
			Metrics:  []string{activity.FormatDuration(matched.DurationSeconds)},
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

// formatWeight renders a weight without a trailing ".0" for whole numbers,
// e.g. 305.0 → "305", 102.5 → "102.5". PR-specific (no exported equivalent in
// the activity card vocabulary, which never renders a bare weight).
func formatWeight(w float64) string {
	if w == float64(int64(w)) {
		return fmt.Sprintf("%d", int64(w))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", w), "0"), ".")
}
