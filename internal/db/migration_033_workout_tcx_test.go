package db

import (
	"context"
	"database/sql"
	"testing"
)

// applyMigrationsThrough applies every migration in version order. Just before
// the migration whose version == pauseAt, it calls before(t, db) so a test can
// seed the pre-state a rebuild migration must preserve. pauseAt == 0 (or no
// matching version) applies everything without pausing.
func applyMigrationsThrough(t *testing.T, db *sql.DB, pauseAt int, before func(t *testing.T, db *sql.DB)) {
	t.Helper()
	ctx := context.Background()
	if err := ensureMigrationsTable(ctx, db); err != nil {
		t.Fatalf("ensure migrations table: %v", err)
	}
	migs, err := collectMigrations(registeredGoMigrations())
	if err != nil {
		t.Fatalf("collect migrations: %v", err)
	}
	for _, m := range migs {
		if m.Version == pauseAt && before != nil {
			before(t, db)
		}
		applied, err := isApplied(ctx, db, m.Version)
		if err != nil {
			t.Fatalf("isApplied %d: %v", m.Version, err)
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			t.Fatalf("apply migration %d (%s): %v", m.Version, m.label(), err)
		}
	}
}

func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("query index %s: %v", name, err)
	}
	return n == 1
}

// TestMigrate033_EmptyDB verifies migration 033 applies cleanly on an empty DB
// and the widened CHECK admits strength_training while still rejecting an
// unknown type.
func TestMigrate033_EmptyDB(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	applyMigrationsThrough(t, db, 0, nil)

	seedUser(t, db, "u1")
	if _, err := db.Exec(`
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds,
			tcx_s3_key, created_at
		) VALUES ('s1', 'u1', 'strength_training', 'manual_tcx', 'src1',
		          '2026-06-19T13:00:00Z', 0, 1800, 'k1', '2026-06-19T13:30:00Z')
	`); err != nil {
		t.Fatalf("insert strength_training activity should succeed: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds,
			tcx_s3_key, created_at
		) VALUES ('s2', 'u1', 'yoga', 'manual_tcx', 'src2',
		          '2026-06-19T13:00:00Z', 0, 1800, 'k2', '2026-06-19T13:30:00Z')
	`); err == nil {
		t.Fatal("insert with unknown activity_type 'yoga' should fail the CHECK")
	}

	for _, idx := range []string{
		"idx_activities_dedup", "idx_activities_user_start",
		"idx_activities_user_type_start", "idx_workouts_activity",
	} {
		if !indexExists(t, db, idx) {
			t.Errorf("index %s missing after migration 033", idx)
		}
	}
}

// TestMigrate033_PreservesPopulatedActivities seeds an activity + trackpoint
// just before the 033 table rebuild and verifies both survive, the FK still
// resolves, and the dedup unique index is intact afterward.
func TestMigrate033_PreservesPopulatedActivities(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	applyMigrationsThrough(t, db, 33, func(t *testing.T, db *sql.DB) {
		seedUser(t, db, "u1")
		seedActivity(t, db, "act1", "u1", "src1")
		if _, err := db.Exec(`
			INSERT INTO activity_trackpoints (
				activity_id, sequence, elapsed_seconds, distance_meters,
				heart_rate_bpm, pace_sec_per_km, elevation_meters
			) VALUES ('act1', 0, 0, 0, 150, 300, 12.5), ('act1', 1, 10, 30, 152, 305, 13.0)
		`); err != nil {
			t.Fatalf("seed trackpoints: %v", err)
		}
	})

	// The activity survived the rebuild.
	var actCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activities WHERE id = 'act1'`).Scan(&actCount); err != nil {
		t.Fatalf("count activity: %v", err)
	}
	if actCount != 1 {
		t.Fatalf("activity survived rebuild: got %d, want 1", actCount)
	}

	// Trackpoints survived and still resolve to the renamed parent.
	var tpCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activity_trackpoints WHERE activity_id = 'act1'`).Scan(&tpCount); err != nil {
		t.Fatalf("count trackpoints: %v", err)
	}
	if tpCount != 2 {
		t.Fatalf("trackpoints survived rebuild: got %d, want 2", tpCount)
	}

	// The dedup unique index is intact: a duplicate (user, source, src id) fails.
	if _, err := db.Exec(`
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds, avg_pace_sec_per_km,
			tcx_s3_key, created_at
		) VALUES ('act2', 'u1', 'running', 'manual_tcx', 'src1',
		          '2026-06-07T07:00:00Z', 10000, 3000, 300, 'k', '2026-06-07T07:30:00Z')
	`); err == nil {
		t.Fatal("duplicate (user, source, source_activity_id) should fail the dedup index")
	}

	if !indexExists(t, db, "idx_workouts_activity") {
		t.Error("idx_workouts_activity missing after migration 033")
	}
}

// TestMigrate033_WorkoutActivityUniqueIndex verifies idx_workouts_activity
// rejects a second live workout pointing at the same activity while allowing
// many NULL links and tolerating a soft-deleted workout's stale link.
func TestMigrate033_WorkoutActivityUniqueIndex(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	applyMigrationsThrough(t, db, 0, nil)
	seedUser(t, db, "u1")

	insertWorkout := func(id string, activityID *string, deleted bool) error {
		deletedAt := sql.NullString{}
		if deleted {
			deletedAt = sql.NullString{String: "2026-06-19T13:00:00Z", Valid: true}
		}
		_, err := db.Exec(`
			INSERT INTO workouts (id, user_id, performed_at, activity_id, created_at, updated_at, deleted_at)
			VALUES (?, 'u1', '2026-06-19T13:00:00Z', ?, '2026-06-19T13:00:00Z', '2026-06-19T13:00:00Z', ?)
		`, id, activityID, deletedAt)
		return err
	}

	act := "act1"
	if err := insertWorkout("w1", &act, false); err != nil {
		t.Fatalf("first live workout linking act1 should succeed: %v", err)
	}
	if err := insertWorkout("w2", &act, false); err == nil {
		t.Fatal("second live workout linking the same activity should fail the unique index")
	}

	// Many NULL activity_id workouts coexist (partial index ignores NULLs).
	if err := insertWorkout("w3", nil, false); err != nil {
		t.Fatalf("null activity_id workout should succeed: %v", err)
	}
	if err := insertWorkout("w4", nil, false); err != nil {
		t.Fatalf("second null activity_id workout should succeed: %v", err)
	}

	// A soft-deleted workout's stale link doesn't block a new live one.
	if err := insertWorkout("w5", &act, true); err != nil {
		t.Fatalf("soft-deleted workout linking act1 should succeed (excluded by partial index): %v", err)
	}
}
