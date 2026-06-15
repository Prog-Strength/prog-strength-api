package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
)

// mustExec runs a statement and fatals on error so test bodies stay readable.
func mustExec(t *testing.T, d *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// TestBackfillTimeline seeds a couple of source rows in each source table,
// runs the backfill, and asserts one feed post per source row was created —
// then re-runs it and asserts the gate keeps it a no-op (idempotent).
func TestBackfillTimeline(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err = db.Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Seed sources directly (raw inserts mirror what the live tables hold).
	// A workout for u1.
	mustExec(t, database, `INSERT INTO workouts (id, user_id, name, performed_at, created_at, updated_at)
		VALUES ('w1', 'u1', 'Push', ?, ?, ?)`, now, now, now)
	// A running activity for u1, with one best effort.
	mustExec(t, database, `INSERT INTO activities
		(id, user_id, activity_type, ingest_source, source_activity_id, start_time,
		 distance_meters, duration_seconds, tcx_s3_key, created_at)
		VALUES ('a1', 'u1', 'running', 'manual_tcx', 'src1', ?, 5000, 1500, 'k1', ?)`, now, now)
	mustExec(t, database, `INSERT INTO activity_best_efforts (activity_id, distance_key, duration_seconds)
		VALUES ('a1', '5k', 1500)`)
	// A PR event for u1. exercise_id/workout_id reference seeded rows.
	mustExec(t, database, `INSERT INTO exercises (id, name, description, created_at, updated_at)
		VALUES ('bench', 'Bench', '', ?, ?)`, now, now)
	mustExec(t, database, `INSERT INTO personal_record_events
		(id, user_id, exercise_id, workout_id, weight, reps, unit, achieved_at, created_at)
		VALUES ('pr1', 'u1', 'bench', 'w1', 225, 3, 'lb', ?, ?)`, now, now)
	// A soft-deleted workout must be skipped.
	mustExec(t, database, `INSERT INTO workouts (id, user_id, name, performed_at, created_at, updated_at, deleted_at)
		VALUES ('wdel', 'u1', 'Deleted', ?, ?, ?, ?)`, now, now, now, now)

	repo := timeline.NewSQLiteRepository(database)

	if err = backfillTimeline(ctx, database, repo); err != nil {
		t.Fatalf("backfillTimeline: %v", err)
	}

	posts, _, err := repo.ListFeed(ctx, []string{"u1"}, "u1", 100, nil)
	if err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	// Expect 4: workout(w1) + run(a1) + best_effort(a1:5k) + pr(pr1).
	// The soft-deleted workout is excluded.
	if len(posts) != 4 {
		t.Fatalf("got %d posts, want 4: %+v", len(posts), posts)
	}
	bySource := map[timeline.SourceType]string{}
	for _, p := range posts {
		bySource[p.SourceType] = p.SourceID
	}
	if bySource[timeline.SourceWorkout] != "w1" {
		t.Errorf("workout post source_id = %q, want w1", bySource[timeline.SourceWorkout])
	}
	if bySource[timeline.SourceRun] != "a1" {
		t.Errorf("run post source_id = %q, want a1", bySource[timeline.SourceRun])
	}
	if bySource[timeline.SourceBestEffort] != "a1:5k" {
		t.Errorf("best_effort post source_id = %q, want a1:5k", bySource[timeline.SourceBestEffort])
	}
	if bySource[timeline.SourcePR] != "pr1" {
		t.Errorf("pr post source_id = %q, want pr1", bySource[timeline.SourcePR])
	}

	// Idempotent re-run: gated on timeline_post being non-empty, so it's a
	// no-op and post count is unchanged.
	if err = backfillTimeline(ctx, database, repo); err != nil {
		t.Fatalf("backfillTimeline (re-run): %v", err)
	}
	posts2, _, err := repo.ListFeed(ctx, []string{"u1"}, "u1", 100, nil)
	if err != nil {
		t.Fatalf("ListFeed (re-run): %v", err)
	}
	if len(posts2) != 4 {
		t.Fatalf("after re-run got %d posts, want 4 (no duplicates)", len(posts2))
	}
}
