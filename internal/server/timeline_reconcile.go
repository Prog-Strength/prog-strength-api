package server

import (
	"context"
	"database/sql"
	"log"

	"github.com/Prog-Strength/prog-strength-api/internal/timeline"
)

// reconcileTimeline makes timeline_post whole: every live activity, PR event,
// and running best effort that has no feed-index row gets one. It runs on
// every boot, not once.
//
// It began life as a one-shot backfill gated on timeline_post being empty —
// the seed for the "no feed" -> "a feed over all history" migration. That gate
// was wrong in a way that took a bug report to surface: publishing is
// deliberately best-effort ("the backfill repairs any gap", timeline.Publisher),
// but a repair that only runs against an empty table repairs nothing after the
// first boot. So a hike logged while the publish hooks were still gated on
// running (fixed in the same change as migration 046) had no post and would
// never get one. Reconciling every boot is what makes best-effort publishing
// actually safe, and it retires the whole class of "the feed missed an event"
// bug — a dropped publish self-heals on the next deploy.
//
// The cost is three NOT EXISTS scans at startup. Each anti-join probes
// timeline_post's UNIQUE(user_id, source_type, source_id) index on a full
// leading-column prefix, so the work is proportional to the source rows, and
// EnsurePost is conflict-safe anyway — the anti-join is there to keep the
// common case (nothing to do) from writing at all.
//
// SQLite-only (called from the SQLite branch in server.New, like the other
// backfills). Reads the source tables with raw SQL (consistent with
// activity/backfill.go) and materializes each result set fully before
// inserting — never inserting inside an open read cursor.
//
// Column/table names were verified against the migrations:
//   - 042_unified_activity_model: activities(id, user_id, activity_type, start_time, deleted_at)
//     — one base table for EVERY session type
//   - 004/042:                    personal_record_events(id, user_id, achieved_at)
//   - 016/042:                    activity_best_efforts(activity_id, distance_key)
func reconcileTimeline(ctx context.Context, db *sql.DB, repo timeline.Repository) error {
	refs, err := collectMissingTimelineRefs(ctx, db)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}

	// Insert after every read cursor is closed. Counts logged per source.
	counts := map[timeline.SourceType]int{}
	for _, ref := range refs {
		if _, err := repo.EnsurePost(ctx, ref); err != nil {
			return err
		}
		counts[ref.SourceType]++
	}

	log.Printf("timeline reconcile: created missing posts activity=%d pr=%d best_effort=%d",
		counts[timeline.SourceActivity], counts[timeline.SourcePR], counts[timeline.SourceBestEffort])
	return nil
}

// collectMissingTimelineRefs reads the source rows that have no timeline_post
// and materializes them into PostRefs before any insert happens. Each query's
// cursor is fully drained and closed inside its own helper closure so no
// EnsurePost runs against an open read.
func collectMissingTimelineRefs(ctx context.Context, db *sql.DB) ([]timeline.PostRef, error) {
	var refs []timeline.PostRef

	// Every live session -> `activity`, occurred_at = start_time. No
	// activity_type filter: the feed carries every registered type, and a
	// type added tomorrow is picked up here with no change to this query.
	if err := scanRefs(ctx, db,
		`SELECT a.id, a.user_id, a.start_time
		 FROM activities a
		 WHERE a.deleted_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM timeline_post p
		       WHERE p.user_id = a.user_id
		         AND p.source_type = 'activity'
		         AND p.source_id = a.id
		   )`,
		func(id, userID string) timeline.PostRef {
			return timeline.PostRef{UserID: userID, SourceType: timeline.SourceActivity, SourceID: id}
		}, &refs); err != nil {
		return nil, err
	}

	// PR events -> `pr`, occurred_at = achieved_at.
	if err := scanRefs(ctx, db,
		`SELECT e.id, e.user_id, e.achieved_at
		 FROM personal_record_events e
		 WHERE NOT EXISTS (
		     SELECT 1 FROM timeline_post p
		     WHERE p.user_id = e.user_id
		       AND p.source_type = 'pr'
		       AND p.source_id = e.id
		 )`,
		func(id, userID string) timeline.PostRef {
			return timeline.PostRef{UserID: userID, SourceType: timeline.SourcePR, SourceID: id}
		}, &refs); err != nil {
		return nil, err
	}

	// Best efforts -> `best_effort`, source_id = "<activity_id>:<distance_key>",
	// occurred_at = the activity's start_time. Running-only, matching where
	// best efforts are computed and published.
	if err := func() error {
		rows, err := db.QueryContext(ctx, `
			SELECT e.activity_id, e.distance_key, a.user_id, a.start_time
			FROM activity_best_efforts e
			JOIN activities a ON a.id = e.activity_id
			WHERE a.deleted_at IS NULL AND a.activity_type = 'running'
			  AND NOT EXISTS (
			      SELECT 1 FROM timeline_post p
			      WHERE p.user_id = a.user_id
			        AND p.source_type = 'best_effort'
			        AND p.source_id = e.activity_id || ':' || e.distance_key
			  )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var activityID, distanceKey, userID string
			var startTime sql.NullTime
			if err := rows.Scan(&activityID, &distanceKey, &userID, &startTime); err != nil {
				return err
			}
			refs = append(refs, timeline.PostRef{
				UserID:     userID,
				SourceType: timeline.SourceBestEffort,
				SourceID:   activityID + ":" + distanceKey,
				OccurredAt: startTime.Time,
			})
		}
		return rows.Err()
	}(); err != nil {
		return nil, err
	}

	return refs, nil
}

// scanRefs runs a (id, user_id, occurred_at) query, drains and closes its
// cursor, and appends one PostRef per row built by mk (which fills
// identity/type; this fills OccurredAt). Shared by the two single-table
// reads above.
func scanRefs(
	ctx context.Context,
	db *sql.DB,
	query string,
	mk func(id, userID string) timeline.PostRef,
	out *[]timeline.PostRef,
) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, userID string
		var occurredAt sql.NullTime
		if err := rows.Scan(&id, &userID, &occurredAt); err != nil {
			return err
		}
		ref := mk(id, userID)
		ref.OccurredAt = occurredAt.Time
		*out = append(*out, ref)
	}
	return rows.Err()
}
