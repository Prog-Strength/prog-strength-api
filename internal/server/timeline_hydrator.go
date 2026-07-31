package server

import (
	"context"
	"fmt"
	"log"
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
	// photoRepo + photoStore decorate session cards with their activity's cover
	// thumbnail and photo count. Both are nil-safe: when photo storage is
	// unconfigured they are nil and session cards render without a photo
	// (graceful degradation). The cover read is one batched query per page.
	photoRepo  activity.PhotoRepository
	photoStore activity.PhotoStore
}

// newTimelineHydrator builds the adapter over the workout + activity repos and
// the type registry (whose descriptors' Summarize renders the session cards).
// photoRepo + photoStore are the activity-photos seam for cover decoration and
// may both be nil when photo storage is unconfigured (cards render photoless).
func newTimelineHydrator(workoutRepo strength.Repository, activityRepo activity.Repository, registry *activity.Registry, photoRepo activity.PhotoRepository, photoStore activity.PhotoStore) *timelineHydrator {
	return &timelineHydrator{workoutRepo: workoutRepo, activityRepo: activityRepo, registry: registry, photoRepo: photoRepo, photoStore: photoStore}
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

	// Every session — a lift, a run, a hike, a future kickboxing class — is
	// an `activity` post pointing at the one activities base table, so they
	// all resolve through a single unified path (registry Summarize). There
	// is deliberately no per-sport branch here: that is what made the feed
	// blind to types nobody had special-cased.
	if err := h.hydrateActivities(ctx, byType[timeline.SourceActivity], out); err != nil {
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

// hydrateActivities renders `activity` posts — every session type — through
// the unified activity store and the type registry: one batched summary read
// (SummariesByIDs, grouped by author since a feed page spans the viewer's
// followees) plus activity.RenderSummaries, which loads each type's detail
// (strength's exercises/sets) in a single batch and renders the same card the
// unified /activities list shows. Nothing here knows which sports exist; a
// newly registered type renders the moment its descriptor does, which is the
// whole point of routing every session through one path. A ref whose source no
// longer resolves is absent from the summaries map and omitted.
//
// The sport travels back out on PostContent.ActivityType so the API's
// discriminator survives the collapse of the per-sport source types, and it
// picks the Href — a web-routing concern that stays here in the wiring layer
// because Summary deliberately carries none.
func (h *timelineHydrator) hydrateActivities(ctx context.Context, refs []timeline.PostRef, out map[timeline.PostRef]timeline.PostContent) error {
	if len(refs) == 0 {
		return nil
	}

	// SummariesByIDs is user-scoped (it enforces ownership), and the feed
	// spans the viewer plus their followees, so batch the base read per author.
	idsByUser := make(map[string][]string)
	for _, ref := range refs {
		idsByUser[ref.UserID] = append(idsByUser[ref.UserID], ref.SourceID)
	}
	activities := make([]activity.Activity, 0, len(refs))
	typeByID := make(map[string]activity.ActivityType, len(refs))
	for uid, ids := range idsByUser {
		found, err := h.activityRepo.SummariesByIDs(ctx, uid, ids)
		if err != nil {
			return err
		}
		for _, a := range found {
			activities = append(activities, a)
			typeByID[a.ID] = a.ActivityType
		}
	}

	// The userID passed to RenderSummaries only feeds each type's batch detail
	// load; strength's is id-scoped (ignores it) and the endurance types
	// summarize off the joined base row, so one render over the merged authors
	// is correct.
	summaries := activity.RenderSummaries(ctx, h.registry, "", activities)

	// One batched cover-photo read for the whole page's renderable session ids
	// (the DB "one query per page" the SOW requires). A cover-load failure must
	// never blank the feed, so it degrades to photoless cards with a log line.
	rendered := make([]timeline.PostRef, 0, len(refs))
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if _, ok := summaries[ref.SourceID]; !ok {
			// Source gone (deleted/not found) or unrenderable: omit.
			continue
		}
		rendered = append(rendered, ref)
		ids = append(ids, ref.SourceID)
	}
	var covers map[string]activity.PhotoCover
	if h.photoRepo != nil && h.photoStore != nil && len(ids) > 0 {
		c, err := h.photoRepo.CoverPhotosByActivityIDs(ctx, ids)
		if err != nil {
			// A photo hiccup shouldn't blank the feed — log and render photoless.
			log.Printf("timeline: cover photo load failed for %d session ids: %v — rendering without photos", len(ids), err)
		} else {
			covers = c
		}
	}

	for _, ref := range rendered {
		s := summaries[ref.SourceID]
		activityType := typeByID[ref.SourceID]
		content := timeline.PostContent{
			Title:        s.Title,
			Subtitle:     s.Subtitle,
			Metrics:      s.Metrics,
			Href:         activityHref(activityType),
			ActivityType: string(activityType),
		}
		if cover, ok := covers[ref.SourceID]; ok {
			// Presign is local HMAC (cheap); per-cover presigning is fine — the
			// SOW's "one query per page" is the DB read above.
			thumbURL, err := h.photoStore.PresignGet(ctx, cover.Cover.ThumbS3Key)
			if err != nil {
				log.Printf("timeline: presign cover thumb for activity %s failed: %v — rendering without photo", ref.SourceID, err)
			} else {
				content.Photo = &timeline.PostPhoto{
					ThumbURL: thumbURL,
					Width:    cover.Cover.Width,
					Height:   cover.Cover.Height,
				}
				content.PhotoCount = cover.Count
			}
		}
		out[ref] = content
	}
	return nil
}

// activityHref maps a session's sport to its web destination — the tab of the
// consolidated Activities page that lists it. There is no per-session detail
// route in web v1. A type with no tab of its own (walking, cycling, other, and
// any newly registered type) lands on the Activities overview, which lists
// every type: an unmapped sport degrades to a working link rather than a 404,
// so adding a type still needs no change here.
func activityHref(t activity.ActivityType) string {
	switch t {
	case activity.ActivityStrengthTraining:
		return "/activities?view=workouts"
	case activity.ActivityRunning:
		return "/activities?view=running"
	case activity.ActivityHiking:
		return "/activities?view=hiking"
	default:
		return "/activities"
	}
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
