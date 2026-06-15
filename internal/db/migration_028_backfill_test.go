package db

import (
	"context"
	"database/sql"
	"testing"
)

// mustExec runs a statement against db and fails the test on error.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// seedPlan inserts a planned_workouts row with the NOT NULL fields set. The
// caller controls kind ('lift'/'run'), scheduled start (RFC3339 UTC), and
// timezone so the same-day + nearest-start rule can be exercised precisely.
func seedPlan(t *testing.T, db *sql.DB, id, userID, kind, startUTC, endUTC, tz string) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO planned_workouts (
			id, user_id, activity_kind, scheduled_start_utc, scheduled_end_utc,
			timezone, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'planned', '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z')
	`, id, userID, kind, startUTC, endUTC, tz)
}

// seedRunActivity inserts a running activity with all NOT NULL columns set so
// the backfill can pick it up as a 'activity'-kind session.
func seedRunActivity(t *testing.T, db *sql.DB, id, userID, startUTC, createdAt string) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds, avg_pace_sec_per_km,
			tcx_s3_key, created_at
		) VALUES (?, ?, 'running', 'manual_tcx', ?, ?, 10000, 3000, 300, ?, ?)
	`, id, userID, "src-"+id, startUTC, "key-"+id, createdAt)
}

// seedWorkout inserts a lifting workout with all NOT NULL columns set so the
// backfill can pick it up as a 'workout'-kind session.
func seedWorkout(t *testing.T, db *sql.DB, id, userID, performedAt, createdAt string) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO workouts (id, user_id, name, performed_at, created_at, updated_at)
		VALUES (?, ?, 'Lift', ?, ?, ?)
	`, id, userID, performedAt, createdAt, createdAt)
}

func planRow(t *testing.T, db *sql.DB, id string) (status, sessionID, sessionKind string) {
	t.Helper()
	var sid, skind sql.NullString
	if err := db.QueryRow(`
		SELECT status, completed_session_id, completed_session_kind
		FROM planned_workouts WHERE id = ?
	`, id).Scan(&status, &sid, &skind); err != nil {
		t.Fatalf("select plan %s: %v", id, err)
	}
	return status, sid.String, skind.String
}

// runBackfill drives backfillPlannedWorkoutLinks inside a manual transaction,
// the same way migration 028 runs it, and commits.
func runBackfill(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := backfillPlannedWorkoutLinks(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestBackfill028_LinksAndIsIdempotent seeds a planned run plus a same-day
// running activity and verifies the plan is completed + linked to the activity,
// then re-runs the backfill to prove it leaves exactly one completed link.
func TestBackfill028_LinksAndIsIdempotent(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	// Plan 17:30 NY-local-day; activity 18:00 UTC -> 14:00 NY, same NY day.
	seedPlan(t, db, "p1", "u1", "run",
		"2026-06-15T17:30:00Z", "2026-06-15T18:30:00Z", "America/New_York")
	seedRunActivity(t, db, "a1", "u1", "2026-06-15T18:00:00Z", "2026-06-15T18:05:00Z")

	runBackfill(t, db)

	status, sid, skind := planRow(t, db, "p1")
	if status != "completed" || sid != "a1" || skind != "activity" {
		t.Fatalf("want completed/a1/activity, got %s/%s/%s", status, sid, skind)
	}

	// Re-run: idempotent. The already-linked activity is excluded by the
	// NOT EXISTS guard, so nothing changes and the plan stays singly linked.
	runBackfill(t, db)

	var completedCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM planned_workouts
		WHERE completed_session_id = 'a1' AND completed_session_kind = 'activity' AND status = 'completed'
	`).Scan(&completedCount); err != nil {
		t.Fatalf("count completed: %v", err)
	}
	if completedCount != 1 {
		t.Fatalf("want exactly one completed link after re-run, got %d", completedCount)
	}
}

// TestBackfill028_FreshDBNoOp verifies running migration 028 over an empty DB
// (no sessions, no plans) is a clean no-op. newMigratedDB already ran 028; this
// re-runs the body directly to confirm the empty-data path errors-free.
func TestBackfill028_FreshDBNoOp(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)
	runBackfill(t, db)
}

// TestBackfill028_WrongKindAndTwoADay verifies that with two planned runs on the
// same NY day, the activity links to the nearest scheduled start, leaving the
// other planned. A workout on that day does not complete a run plan.
func TestBackfill028_WrongKindAndTwoADay(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	// All three plan starts and the activity are on the same NY calendar day.
	seedPlan(t, db, "early", "u1", "run",
		"2026-06-15T15:00:00Z", "2026-06-15T16:00:00Z", "America/New_York")
	seedPlan(t, db, "late", "u1", "run",
		"2026-06-15T22:00:00Z", "2026-06-15T23:00:00Z", "America/New_York")
	// Activity at 21:30Z is 30m from 'late' and 6h30m from 'early'.
	seedRunActivity(t, db, "a1", "u1", "2026-06-15T21:30:00Z", "2026-06-15T21:35:00Z")
	// A workout the same day must NOT complete a run plan (wrong kind).
	seedWorkout(t, db, "w1", "u1", "2026-06-15T16:00:00Z", "2026-06-15T16:05:00Z")

	runBackfill(t, db)

	if status, sid, skind := planRow(t, db, "late"); status != "completed" || sid != "a1" || skind != "activity" {
		t.Fatalf("want late completed/a1/activity, got %s/%s/%s", status, sid, skind)
	}
	if status, _, _ := planRow(t, db, "early"); status != "planned" {
		t.Fatalf("want early still planned, got %s", status)
	}
}

// TestBackfill028_LiftLinks verifies a planned lift links to a same-day workout
// with kind 'workout'.
func TestBackfill028_LiftLinks(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedPlan(t, db, "lp", "u1", "lift",
		"2026-06-15T17:30:00Z", "2026-06-15T18:30:00Z", "America/New_York")
	seedWorkout(t, db, "w1", "u1", "2026-06-15T18:00:00Z", "2026-06-15T18:05:00Z")

	runBackfill(t, db)

	if status, sid, skind := planRow(t, db, "lp"); status != "completed" || sid != "w1" || skind != "workout" {
		t.Fatalf("want lift completed/w1/workout, got %s/%s/%s", status, sid, skind)
	}
}

// TestBackfill028_TimezoneBoundary verifies the same-local-day rule uses the
// plan's IANA timezone, not UTC: a NY plan at 03:00Z (= 23:00 the previous NY
// day) does NOT match an activity logged the next UTC day, even though both are
// the same UTC calendar day.
func TestBackfill028_TimezoneBoundary(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	// 2026-06-16T03:00:00Z is 2026-06-15 23:00 in NY (previous local day).
	seedPlan(t, db, "p1", "u1", "run",
		"2026-06-16T03:00:00Z", "2026-06-16T04:00:00Z", "America/New_York")
	// Activity 2026-06-16T12:00:00Z is 2026-06-16 08:00 NY -> different NY day.
	seedRunActivity(t, db, "a1", "u1", "2026-06-16T12:00:00Z", "2026-06-16T12:05:00Z")

	runBackfill(t, db)

	if status, _, _ := planRow(t, db, "p1"); status != "planned" {
		t.Fatalf("want plan still planned (different NY local day), got %s", status)
	}
}
