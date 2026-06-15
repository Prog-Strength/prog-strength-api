package server

import (
	"context"
	"database/sql"
	"log"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
)

// backfillTimeline seeds timeline_post from existing workouts, runs, PR
// events, and best efforts for the one-time migration from "no feed" to "a
// feed index over all history". SQLite-only (called from the SQLite branch in
// server.New, like the other backfills).
//
// Idempotent two ways: gated on timeline_post being empty so it runs once
// after migration 019 ships and is a no-op on every subsequent boot, and
// EnsurePost itself is conflict-safe so even a forced re-run can't duplicate
// posts. Reads the source tables with raw SQL (consistent with
// activity/backfill.go) and materializes each result set fully before
// inserting — never inserting inside an open read cursor.
//
// Column/table names were verified against the migrations:
//   - 001_initial_schema.sql:    workouts(id, user_id, performed_at, deleted_at)
//   - 015_activities_generalize: activities(id, user_id, activity_type, start_time, deleted_at)
//   - 004_personal_records.sql:  personal_record_events(id, user_id, achieved_at)
//   - 016_activity_best_efforts: activity_best_efforts(activity_id, distance_key)
func backfillTimeline(ctx context.Context, db *sql.DB, repo timeline.Repository) error {
	var existing int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM timeline_post`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		// Already seeded — nothing to do.
		return nil
	}

	refs, err := collectTimelineBackfillRefs(ctx, db)
	if err != nil {
		return err
	}

	// Insert after every read cursor is closed. Counts logged per source.
	counts := map[timeline.SourceType]int{}
	for _, ref := range refs {
		if _, err := repo.EnsurePost(ctx, ref); err != nil {
			return err
		}
		counts[ref.SourceType]++
	}

	log.Printf("timeline backfill: seeded posts workout=%d run=%d pr=%d best_effort=%d",
		counts[timeline.SourceWorkout], counts[timeline.SourceRun],
		counts[timeline.SourcePR], counts[timeline.SourceBestEffort])
	return nil
}

// collectTimelineBackfillRefs reads all source rows and materializes them into
// PostRefs before any insert happens. Each query's cursor is fully drained and
// closed inside its own helper closure so no EnsurePost runs against an open
// read.
func collectTimelineBackfillRefs(ctx context.Context, db *sql.DB) ([]timeline.PostRef, error) {
	var refs []timeline.PostRef

	// Workouts → `workout`, occurred_at = performed_at.
	if err := scanRefs(ctx, db,
		`SELECT id, user_id, performed_at FROM workouts WHERE deleted_at IS NULL`,
		func(id, userID string) timeline.PostRef {
			return timeline.PostRef{UserID: userID, SourceType: timeline.SourceWorkout, SourceID: id}
		}, &refs); err != nil {
		return nil, err
	}

	// Running activities → `run`, occurred_at = start_time.
	if err := scanRefs(ctx, db,
		`SELECT id, user_id, start_time FROM activities WHERE activity_type = 'running' AND deleted_at IS NULL`,
		func(id, userID string) timeline.PostRef {
			return timeline.PostRef{UserID: userID, SourceType: timeline.SourceRun, SourceID: id}
		}, &refs); err != nil {
		return nil, err
	}

	// PR events → `pr`, occurred_at = achieved_at.
	if err := scanRefs(ctx, db,
		`SELECT id, user_id, achieved_at FROM personal_record_events`,
		func(id, userID string) timeline.PostRef {
			return timeline.PostRef{UserID: userID, SourceType: timeline.SourcePR, SourceID: id}
		}, &refs); err != nil {
		return nil, err
	}

	// Best efforts → `best_effort`, source_id = "<activity_id>:<distance_key>",
	// occurred_at = the activity's start_time.
	if err := func() error {
		rows, err := db.QueryContext(ctx, `
			SELECT e.activity_id, e.distance_key, a.user_id, a.start_time
			FROM activity_best_efforts e
			JOIN activities a ON a.id = e.activity_id
			WHERE a.deleted_at IS NULL AND a.activity_type = 'running'
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
// identity/type; this fills OccurredAt). Shared by the three single-table
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
