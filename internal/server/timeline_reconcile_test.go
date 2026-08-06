package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/db"
	"github.com/Prog-Strength/prog-strength-api/internal/timeline"
)

// mustExec runs a statement and fatals on error so test bodies stay readable.
func mustExec(t *testing.T, d *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// newReconcileDB opens a migrated scratch database for the reconcile tests.
func newReconcileDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// TestReconcileTimeline seeds a row in each source table, reconciles, and
// asserts one feed post per source row — then re-runs and asserts no
// duplicates. Every session type lands under the one `activity` source type.
func TestReconcileTimeline(t *testing.T) {
	ctx := context.Background()
	database := newReconcileDB(t)

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Seed sources directly (raw inserts mirror what the live tables hold).
	// A workout for u1.
	mustExec(t, database, `INSERT INTO activities (id, user_id, activity_type, ingest_source, name, start_time, created_at, updated_at)
		VALUES ('w1', 'u1', 'strength_training', 'manual', 'Push', ?, ?, ?)`, now, now, now)
	// A running activity for u1, with one best effort.
	mustExec(t, database, `INSERT INTO activities
		(id, user_id, activity_type, ingest_source, source_activity_id, start_time,
		 duration_seconds, tcx_s3_key, created_at, updated_at)
		VALUES ('a1', 'u1', 'running', 'manual_tcx', 'src1', ?, 1500, 'k1', ?, ?)`, now, now, now)
	mustExec(t, database, `INSERT INTO activity_run_details (activity_id, distance_meters, raw_distance_meters)
		VALUES ('a1', 5000, 5000)`)
	mustExec(t, database, `INSERT INTO activity_best_efforts (activity_id, distance_key, duration_seconds)
		VALUES ('a1', '5k', 1500)`)
	// A PR event for u1. exercise_id/workout_id reference seeded rows.
	mustExec(t, database, `INSERT INTO exercises (id, name, description, created_at, updated_at)
		VALUES ('bench', 'Bench', '', ?, ?)`, now, now)
	mustExec(t, database, `INSERT INTO personal_record_events
		(id, user_id, exercise_id, activity_id, weight, reps, unit, achieved_at, created_at)
		VALUES ('pr1', 'u1', 'bench', 'w1', 225, 3, 'lb', ?, ?)`, now, now)
	// A soft-deleted workout must be skipped.
	mustExec(t, database, `INSERT INTO activities (id, user_id, activity_type, ingest_source, name, start_time, created_at, updated_at, deleted_at)
		VALUES ('wdel', 'u1', 'strength_training', 'manual', 'Deleted', ?, ?, ?, ?)`, now, now, now, now)

	repo := timeline.NewSQLiteRepository(database)

	if err := reconcileTimeline(ctx, database, repo); err != nil {
		t.Fatalf("reconcileTimeline: %v", err)
	}

	posts, _, err := repo.ListFeed(ctx, []string{"u1"}, "u1", 100, nil)
	if err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	// Expect 4: activity(w1) + activity(a1) + best_effort(a1:5k) + pr(pr1).
	// The soft-deleted workout is excluded.
	if len(posts) != 4 {
		t.Fatalf("got %d posts, want 4: %+v", len(posts), posts)
	}
	activityIDs := map[string]bool{}
	bySource := map[timeline.SourceType]string{}
	for _, p := range posts {
		if p.SourceType == timeline.SourceActivity {
			activityIDs[p.SourceID] = true
			continue
		}
		bySource[p.SourceType] = p.SourceID
	}
	if !activityIDs["w1"] || !activityIDs["a1"] || len(activityIDs) != 2 {
		t.Errorf("activity posts = %v, want exactly w1 and a1", activityIDs)
	}
	if bySource[timeline.SourceBestEffort] != "a1:5k" {
		t.Errorf("best_effort post source_id = %q, want a1:5k", bySource[timeline.SourceBestEffort])
	}
	if bySource[timeline.SourcePR] != "pr1" {
		t.Errorf("pr post source_id = %q, want pr1", bySource[timeline.SourcePR])
	}

	// Idempotent re-run: the anti-join finds nothing missing, and EnsurePost
	// is conflict-safe regardless, so the post count is unchanged.
	if err = reconcileTimeline(ctx, database, repo); err != nil {
		t.Fatalf("reconcileTimeline (re-run): %v", err)
	}
	posts2, _, err := repo.ListFeed(ctx, []string{"u1"}, "u1", 100, nil)
	if err != nil {
		t.Fatalf("ListFeed (re-run): %v", err)
	}
	if len(posts2) != 4 {
		t.Fatalf("after re-run got %d posts, want 4 (no duplicates)", len(posts2))
	}
}

// TestReconcileTimeline_RepairsGapInPopulatedFeed is the regression guard for
// the reported bug. The old backfill was gated on timeline_post being EMPTY, so
// a hike logged into an already-populated feed — which is what every hike was,
// since the publish hooks skipped non-running types — could never be repaired.
// Reconciling has to create the missing post even though the table is full of
// other posts.
func TestReconcileTimeline_RepairsGapInPopulatedFeed(t *testing.T) {
	ctx := context.Background()
	database := newReconcileDB(t)
	repo := timeline.NewSQLiteRepository(database)

	now := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)

	// An already-posted run: the feed is NOT empty.
	mustExec(t, database, `INSERT INTO activities
		(id, user_id, activity_type, ingest_source, source_activity_id, start_time,
		 duration_seconds, tcx_s3_key, created_at, updated_at)
		VALUES ('a1', 'u1', 'running', 'manual_tcx', 'src1', ?, 1500, 'k1', ?, ?)`, now, now, now)
	if _, err := repo.EnsurePost(ctx, timeline.PostRef{
		UserID: "u1", SourceType: timeline.SourceActivity, SourceID: "a1", OccurredAt: now,
	}); err != nil {
		t.Fatalf("seed run post: %v", err)
	}

	// The hike that never got a post.
	mustExec(t, database, `INSERT INTO activities (id, user_id, activity_type, ingest_source, name, start_time, created_at, updated_at)
		VALUES ('h1', 'u1', 'hiking', 'manual', 'Franconia Ridge', ?, ?, ?)`, now, now, now)

	if err := reconcileTimeline(ctx, database, repo); err != nil {
		t.Fatalf("reconcileTimeline: %v", err)
	}

	posts, _, err := repo.ListFeed(ctx, []string{"u1"}, "u1", 100, nil)
	if err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	ids := map[string]bool{}
	for _, p := range posts {
		ids[p.SourceID] = true
	}
	if !ids["h1"] {
		t.Errorf("hike h1 has no feed post after reconcile; posts = %v", ids)
	}
	if len(posts) != 2 {
		t.Errorf("got %d posts, want 2 (the run kept, the hike repaired)", len(posts))
	}
}

// TestReconcileTimeline_PreservesOccurredAt pins that a repaired post orders by
// the session's start time, not the time the repair ran — a post inserted with
// "now" would jump to the top of the feed on every deploy that repairs it.
func TestReconcileTimeline_PreservesOccurredAt(t *testing.T) {
	ctx := context.Background()
	database := newReconcileDB(t)
	repo := timeline.NewSQLiteRepository(database)

	started := time.Date(2025, 9, 14, 6, 30, 0, 0, time.UTC)
	mustExec(t, database, `INSERT INTO activities (id, user_id, activity_type, ingest_source, name, start_time, created_at, updated_at)
		VALUES ('h1', 'u1', 'hiking', 'manual', 'Old hike', ?, ?, ?)`, started, started, started)

	if err := reconcileTimeline(ctx, database, repo); err != nil {
		t.Fatalf("reconcileTimeline: %v", err)
	}
	posts, _, err := repo.ListFeed(ctx, []string{"u1"}, "u1", 100, nil)
	if err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	if !posts[0].OccurredAt.Equal(started) {
		t.Errorf("occurred_at = %s, want the session start %s", posts[0].OccurredAt, started)
	}
}
