package db

import (
	"database/sql"
	"testing"
)

// seed043Fixture builds the pre-043 world at schema 42 with raw SQL: two
// completed plans linked to sessions under BOTH recorded discriminator values
// (pw1 kind='workout', pw2 kind='activity' — the mix migration 042 leaves
// behind), each carrying a full agenda (exercise + set) so the rebuild is
// proven to preserve the children, plus one bare planned (uncompleted) plan.
func seed043Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrateUpTo(db, 42); err != nil {
		t.Fatalf("migrate up to 042: %v", err)
	}
	seedUser(t, db, "u1")
	mustSeed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	// pw1: completed by a strength session, recorded kind 'workout' (the value
	// the live write path stamped before this cleanup). Full lift agenda.
	mustSeed(`INSERT INTO planned_workouts (id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
			timezone, status, notes, completed_session_id, completed_session_kind, created_at, updated_at)
		VALUES ('pw1', 'u1', 'Planned Push', 'lift', '2026-06-01T10:00:00Z', '2026-06-01T11:00:00Z',
		        'UTC', 'completed', 'plan notes', 'w1', 'workout', '2026-05-30T00:00:00Z', '2026-05-30T00:00:00Z')`)
	mustSeed(`INSERT INTO planned_workout_exercises (id, planned_workout_id, exercise_id, order_index, notes, superset_group)
		VALUES ('pe1', 'pw1', 'bench', 0, 'agenda note', 2)`)
	mustSeed(`INSERT INTO planned_sets (id, planned_workout_exercise_id, order_index, target_reps, target_weight, unit, target_rpe, amrap)
		VALUES ('ps1', 'pe1', 0, 5, 185, 'lb', 8, 0),
		       ('ps2', 'pe1', 1, NULL, NULL, NULL, NULL, 1)`)

	// pw2: completed by a running session, recorded kind 'activity'. Run agenda
	// (run_type/run_details), no exercises.
	mustSeed(`INSERT INTO planned_workouts (id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
			timezone, status, completed_session_id, completed_session_kind, run_type, run_details, created_at, updated_at)
		VALUES ('pw2', 'u1', 'Planned Run', 'run', '2026-06-02T07:00:00Z', '2026-06-02T08:00:00Z',
		        'UTC', 'completed', 'a1', 'activity', 'threshold', '4x1mi', '2026-05-31T00:00:00Z', '2026-05-31T00:00:00Z')`)

	// pw3: bare planned lift, no completion link.
	mustSeed(`INSERT INTO planned_workouts (id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
			timezone, status, created_at, updated_at)
		VALUES ('pw3', 'u1', 'Future Day', 'lift', '2026-06-10T10:00:00Z', '2026-06-10T11:00:00Z',
		        'UTC', 'planned', '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z')`)
}

func TestMigrate043_DropCompletedSessionKind(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	seed043Fixture(t, db)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate through 043: %v", err)
	}

	t.Run("column dropped", func(t *testing.T) {
		if got := queryInt(t, db, `SELECT COUNT(*) FROM pragma_table_info('planned_workouts') WHERE name = 'completed_session_kind'`); got != 0 {
			t.Errorf("completed_session_kind still present (%d cols)", got)
		}
	})

	t.Run("plans survive minus the column", func(t *testing.T) {
		if got := queryInt(t, db, `SELECT COUNT(*) FROM planned_workouts`); got != 3 {
			t.Fatalf("planned_workouts rows = %d, want 3", got)
		}
		// pw1: completion link + status + notes preserved.
		var status, sessionID, notes string
		if err := db.QueryRow(`SELECT status, completed_session_id, notes FROM planned_workouts WHERE id = 'pw1'`).
			Scan(&status, &sessionID, &notes); err != nil {
			t.Fatalf("read pw1: %v", err)
		}
		if status != "completed" || sessionID != "w1" || notes != "plan notes" {
			t.Errorf("pw1 = (%q, %q, %q), want (completed, w1, plan notes)", status, sessionID, notes)
		}
		// pw2: run agenda + completion link preserved.
		var runType, runDetails, pw2Session string
		if err := db.QueryRow(`SELECT run_type, run_details, completed_session_id FROM planned_workouts WHERE id = 'pw2'`).
			Scan(&runType, &runDetails, &pw2Session); err != nil {
			t.Fatalf("read pw2: %v", err)
		}
		if runType != "threshold" || runDetails != "4x1mi" || pw2Session != "a1" {
			t.Errorf("pw2 = (%q, %q, %q), want (threshold, 4x1mi, a1)", runType, runDetails, pw2Session)
		}
	})

	t.Run("agenda children survive the rebuild", func(t *testing.T) {
		if got := queryInt(t, db, `SELECT COUNT(*) FROM planned_workout_exercises WHERE planned_workout_id = 'pw1'`); got != 1 {
			t.Errorf("pw1 exercises = %d, want 1", got)
		}
		var notes string
		var superset sql.NullInt64
		if err := db.QueryRow(`SELECT notes, superset_group FROM planned_workout_exercises WHERE id = 'pe1'`).
			Scan(&notes, &superset); err != nil {
			t.Fatalf("read pe1: %v", err)
		}
		if notes != "agenda note" || !superset.Valid || superset.Int64 != 2 {
			t.Errorf("pe1 = (%q, %v), want (agenda note, 2)", notes, superset)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM planned_sets WHERE planned_workout_exercise_id = 'pe1'`); got != 2 {
			t.Errorf("pe1 sets = %d, want 2", got)
		}
		// The AMRAP set carries amrap=1 (proves the column round-tripped).
		if got := queryInt(t, db, `SELECT amrap FROM planned_sets WHERE id = 'ps2'`); got != 1 {
			t.Errorf("ps2 amrap = %d, want 1", got)
		}
	})

	t.Run("foreign keys clean and re-pointed", func(t *testing.T) {
		// Exercises FK target re-points to the canonical name after RENAME.
		var target string
		if err := db.QueryRow(`SELECT "table" FROM pragma_foreign_key_list('planned_workout_exercises')`).Scan(&target); err != nil {
			t.Fatalf("foreign_key_list(planned_workout_exercises): %v", err)
		}
		if target != "planned_workouts" {
			t.Errorf("planned_workout_exercises FK target = %q, want planned_workouts", target)
		}
		rows, err := db.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatalf("foreign_key_check: %v", err)
		}
		defer rows.Close()
		if rows.Next() {
			t.Error("foreign_key_check returned rows, want none")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("foreign_key_check rows: %v", err)
		}
	})

	t.Run("cascade still intact through the rebuilt chain", func(t *testing.T) {
		// Hard-deleting a plan takes its exercises and sets with it — proving
		// the rebuilt FKs actually resolve against the renamed tables.
		if _, err := db.Exec(`DELETE FROM planned_workouts WHERE id = 'pw1'`); err != nil {
			t.Fatalf("hard delete pw1: %v", err)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM planned_workout_exercises WHERE planned_workout_id = 'pw1'`); got != 0 {
			t.Errorf("pw1 exercises after delete = %d, want cascaded 0", got)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM planned_sets WHERE planned_workout_exercise_id = 'pe1'`); got != 0 {
			t.Errorf("pe1 sets after delete = %d, want cascaded 0", got)
		}
	})

	t.Run("schema identical to a fresh database", func(t *testing.T) {
		fresh := newMigratedDB(t)
		for _, table := range []string{"planned_workouts", "planned_workout_exercises", "planned_sets"} {
			migrated := queryString(t, db, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
			scratch := queryString(t, fresh, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
			if migrated != scratch {
				t.Errorf("DDL for %s diverges between migrated and fresh DBs:\nmigrated: %s\nfresh:    %s", table, migrated, scratch)
			}
		}
	})
}
