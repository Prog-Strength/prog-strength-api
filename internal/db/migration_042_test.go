package db

import (
	"database/sql"
	"testing"
)

// seed042Fixture builds the pre-042 world at schema 041 with raw SQL:
//
//	w1  ended workout, 2 exercises x 2 sets, PR + PR event + 1RM row,
//	    memory_distilled_at set, one workout_note agent memory
//	w2  end-less workout, no enrichment, empty-string name
//	w3  workout linked to strength_training enrichment activity a3
//	    (vitals + tcx_s3_key + 3 trackpoints), no ended_at
//	a1  running activity with the full endurance column set,
//	    2 trackpoints, 1 best effort
//	a2  walking activity
//	timeline posts for w1 ('workout') and a1 ('run')
//	planned workout completed by w1 with completed_session_kind='workout'
func seed042Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrateUpTo(db, 41); err != nil {
		t.Fatalf("migrate up to 041: %v", err)
	}
	seedUser(t, db, "u1")
	mustSeed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	mustSeed(`INSERT INTO exercises (id, name, created_at, updated_at)
		VALUES ('bench', 'Bench Press', '2026-01-01', '2026-01-01'),
		       ('squat', 'Back Squat', '2026-01-01', '2026-01-01')`)

	// w1: ended, exercises + sets + derived stat rows.
	mustSeed(`INSERT INTO workouts (id, user_id, name, performed_at, ended_at, notes, created_at, updated_at, memory_distilled_at)
		VALUES ('w1', 'u1', 'Push Day', '2026-06-01T10:00:00Z', '2026-06-01T11:00:00Z', 'felt good',
		        '2026-06-01T11:05:00Z', '2026-06-01T11:06:00Z', '2026-06-02T00:00:00Z')`)
	mustSeed(`INSERT INTO workout_exercises (id, workout_id, exercise_id, exercise_order, notes, superset_group)
		VALUES (11, 'w1', 'bench', 0, 'we note', 1), (12, 'w1', 'squat', 1, NULL, NULL)`)
	mustSeed(`INSERT INTO sets (id, workout_exercise_id, reps, weight, unit, set_order)
		VALUES (101, 11, 5, 185, 'lb', 0), (102, 11, 5, 195, 'lb', 1),
		       (103, 12, 3, 275, 'lb', 0), (104, 12, 3, 285, 'lb', 1)`)
	mustSeed(`INSERT INTO personal_records (id, user_id, exercise_id, workout_id, weight, reps, unit, achieved_at, created_at, updated_at)
		VALUES ('pr1', 'u1', 'bench', 'w1', 195, 5, 'lb', '2026-06-01T10:00:00Z', '2026-06-01T11:05:00Z', '2026-06-01T11:05:00Z')`)
	mustSeed(`INSERT INTO personal_record_events (id, user_id, exercise_id, workout_id, weight, reps, unit,
			previous_weight, previous_reps, previous_unit, achieved_at, created_at)
		VALUES ('pre1', 'u1', 'bench', 'w1', 195, 5, 'lb', 185, 5, 'lb', '2026-06-01T10:00:00Z', '2026-06-01T11:05:00Z')`)
	mustSeed(`INSERT INTO exercise_one_rep_max_history (id, user_id, workout_id, exercise_id, performed_at,
			min_estimated_1rm, avg_estimated_1rm, max_estimated_1rm, set_count, unit, created_at, updated_at)
		VALUES ('orm1', 'u1', 'w1', 'bench', '2026-06-01T10:00:00Z', 215.8, 221.6, 227.5, 2, 'lb',
		        '2026-06-01T11:05:00Z', '2026-06-01T11:05:00Z')`)
	mustSeed(`INSERT INTO agent_memories (id, user_id, distilled_text, source_type, source_workout_id,
			embedding_model, embedding_dim, created_at)
		VALUES (1, 'u1', 'left shoulder cranky on bench', 'workout_note', 'w1', 'test-model', 4, '2026-06-02T00:00:00Z')`)

	// w2: no ended_at, no enrichment, empty-string name.
	mustSeed(`INSERT INTO workouts (id, user_id, name, performed_at, notes, created_at, updated_at)
		VALUES ('w2', 'u1', '', '2026-06-03T09:00:00Z', NULL, '2026-06-03T09:00:00Z', '2026-06-03T09:00:00Z')`)

	// w3 + a3: linked strength_training enrichment (vitals, tcx, trackpoints).
	mustSeed(`INSERT INTO activities (id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, name, distance_meters, duration_seconds, avg_heart_rate_bpm, max_heart_rate_bpm,
			total_calories, tcx_s3_key, created_at, environment, raw_distance_meters)
		VALUES ('a3', 'u1', 'strength_training', 'manual_tcx', 'garmin-a3',
		        '2026-06-05T18:00:00Z', NULL, 0, 2400, 141, 176, 310, 'keys/a3.tcx',
		        '2026-06-05T19:00:00Z', 'outdoor', 0)`)
	mustSeed(`INSERT INTO activity_trackpoints (activity_id, sequence, elapsed_seconds, distance_meters, heart_rate_bpm)
		VALUES ('a3', 0, 0, 0, 120), ('a3', 1, 60, 0, 150), ('a3', 2, 120, 0, 165)`)
	mustSeed(`INSERT INTO workouts (id, user_id, name, performed_at, notes, activity_id, created_at, updated_at)
		VALUES ('w3', 'u1', 'Enriched Day', '2026-06-05T18:00:00Z', NULL, 'a3', '2026-06-05T19:00:00Z', '2026-06-05T19:00:00Z')`)

	// a1: running with the full endurance column set + trackpoints + best effort.
	mustSeed(`INSERT INTO activities (id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, name, distance_meters, duration_seconds, avg_pace_sec_per_km, best_pace_sec_per_km,
			avg_heart_rate_bpm, max_heart_rate_bpm, total_calories, elevation_gain_meters, tcx_s3_key,
			created_at, environment, raw_distance_meters, route_geojson)
		VALUES ('a1', 'u1', 'running', 'manual_tcx', 'garmin-a1',
		        '2026-06-07T07:00:00Z', 'Morning Run', 10000, 3000, 300, 280,
		        152, 181, 512, 42.5, 'keys/a1.tcx',
		        '2026-06-07T08:00:00Z', 'outdoor', 10100, '{"type":"Feature"}')`)
	mustSeed(`INSERT INTO activity_trackpoints (activity_id, sequence, elapsed_seconds, distance_meters, heart_rate_bpm, pace_sec_per_km, elevation_meters, latitude, longitude)
		VALUES ('a1', 0, 0, 0, 140, 305, 12.5, 40.1, -75.2), ('a1', 1, 30, 100, 150, 298, 12.7, 40.2, -75.3)`)
	mustSeed(`INSERT INTO activity_best_efforts (activity_id, distance_key, duration_seconds, window_start_elapsed_seconds, window_end_elapsed_seconds)
		VALUES ('a1', '5k', 1184.7, 10, 1194.7)`)

	// a2: walking.
	mustSeed(`INSERT INTO activities (id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds, tcx_s3_key, created_at, environment, raw_distance_meters)
		VALUES ('a2', 'u1', 'walking', 'manual_tcx', 'garmin-a2',
		        '2026-06-08T07:00:00Z', 2000, 1500, 'keys/a2.tcx', '2026-06-08T08:00:00Z', 'indoor', 2000)`)

	// Timeline posts (must survive byte-identical).
	mustSeed(`INSERT INTO timeline_post (id, user_id, source_type, source_id, occurred_at, visibility, created_at, updated_at)
		VALUES ('tp1', 'u1', 'workout', 'w1', '2026-06-01T10:00:00Z', 'private', '2026-06-01T11:05:00Z', '2026-06-01T11:05:00Z'),
		       ('tp2', 'u1', 'run', 'a1', '2026-06-07T07:00:00Z', 'private', '2026-06-07T08:00:00Z', '2026-06-07T08:00:00Z')`)

	// Planned workout completed by w1 under the old 'workout' kind.
	mustSeed(`INSERT INTO planned_workouts (id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
			timezone, status, completed_session_id, completed_session_kind, created_at, updated_at)
		VALUES ('pw1', 'u1', 'Planned Push', 'lift', '2026-06-01T10:00:00Z', '2026-06-01T11:00:00Z',
		        'UTC', 'completed', 'w1', 'workout', '2026-05-30T00:00:00Z', '2026-05-30T00:00:00Z')`)
}

func queryString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var v string
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func queryInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var v int
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	return queryInt(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name) == 1
}

func TestMigrate042_UnifiedActivityModel(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	seed042Fixture(t, db)
	// Pin at 042: this test asserts 042's own end state, including the
	// completed_session_kind normalization that migration 043 later drops.
	if err := migrateUpTo(db, 42); err != nil {
		t.Fatalf("migrate through 042: %v", err)
	}

	t.Run("base membership and folded columns", func(t *testing.T) {
		for _, id := range []string{"w1", "w2", "w3", "a1", "a2"} {
			if queryInt(t, db, `SELECT COUNT(*) FROM activities WHERE id = ?`, id) != 1 {
				t.Errorf("activities missing %s", id)
			}
		}
		if queryInt(t, db, `SELECT COUNT(*) FROM activities WHERE id = 'a3'`) != 0 {
			t.Error("enrichment row a3 should have been folded into w3, not carried")
		}

		// w1: type/start/duration from the workout itself.
		var actType, start string
		var dur sql.NullInt64
		var name, notes sql.NullString
		if err := db.QueryRow(`SELECT activity_type, start_time, duration_seconds, name, notes FROM activities WHERE id = 'w1'`).
			Scan(&actType, &start, &dur, &name, &notes); err != nil {
			t.Fatalf("read w1: %v", err)
		}
		if actType != "strength_training" {
			t.Errorf("w1 activity_type = %q", actType)
		}
		if start != "2026-06-01T10:00:00Z" {
			t.Errorf("w1 start_time = %q, want old performed_at", start)
		}
		if !dur.Valid || dur.Int64 != 3600 {
			t.Errorf("w1 duration_seconds = %v, want 3600 (ended-performed)", dur)
		}
		if !name.Valid || name.String != "Push Day" {
			t.Errorf("w1 name = %v", name)
		}
		if !notes.Valid || notes.String != "felt good" {
			t.Errorf("w1 notes = %v", notes)
		}
		if got := queryString(t, db, `SELECT ingest_source FROM activities WHERE id = 'w1'`); got != "manual" {
			t.Errorf("w1 ingest_source = %q, want manual", got)
		}
		if got := queryString(t, db, `SELECT memory_distilled_at FROM activities WHERE id = 'w1'`); got == "" {
			t.Error("w1 memory_distilled_at should carry over")
		}

		// w2: NULL duration and NULLIF('') name.
		var w2dur sql.NullInt64
		var w2name sql.NullString
		if err := db.QueryRow(`SELECT duration_seconds, name FROM activities WHERE id = 'w2'`).Scan(&w2dur, &w2name); err != nil {
			t.Fatalf("read w2: %v", err)
		}
		if w2dur.Valid {
			t.Errorf("w2 duration_seconds = %d, want NULL (end-less)", w2dur.Int64)
		}
		if w2name.Valid {
			t.Errorf("w2 name = %q, want NULL (empty string folded)", w2name.String)
		}

		// w3: carries a3's vitals + tcx provenance + duration.
		var avgHR, maxHR, cal, w3dur sql.NullInt64
		var ingest string
		var srcID, tcxKey sql.NullString
		if err := db.QueryRow(`SELECT avg_heart_rate_bpm, max_heart_rate_bpm, total_calories, duration_seconds,
				ingest_source, source_activity_id, tcx_s3_key
			FROM activities WHERE id = 'w3'`).Scan(&avgHR, &maxHR, &cal, &w3dur, &ingest, &srcID, &tcxKey); err != nil {
			t.Fatalf("read w3: %v", err)
		}
		if !avgHR.Valid || avgHR.Int64 != 141 || !maxHR.Valid || maxHR.Int64 != 176 || !cal.Valid || cal.Int64 != 310 {
			t.Errorf("w3 vitals = %v/%v/%v, want 141/176/310", avgHR, maxHR, cal)
		}
		if !w3dur.Valid || w3dur.Int64 != 2400 {
			t.Errorf("w3 duration_seconds = %v, want a3's 2400", w3dur)
		}
		if ingest != "manual_tcx" {
			t.Errorf("w3 ingest_source = %q, want manual_tcx", ingest)
		}
		if !srcID.Valid || srcID.String != "garmin-a3" {
			t.Errorf("w3 source_activity_id = %v, want garmin-a3", srcID)
		}
		if !tcxKey.Valid || tcxKey.String != "keys/a3.tcx" {
			t.Errorf("w3 tcx_s3_key = %v, want keys/a3.tcx", tcxKey)
		}
	})

	t.Run("trackpoints re-keyed", func(t *testing.T) {
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_trackpoints WHERE activity_id = 'w3'`); got != 3 {
			t.Errorf("w3 trackpoints = %d, want a3's 3", got)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_trackpoints WHERE activity_id = 'a3'`); got != 0 {
			t.Errorf("a3 trackpoints = %d, want 0 (re-keyed)", got)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_trackpoints WHERE activity_id = 'a1'`); got != 2 {
			t.Errorf("a1 trackpoints = %d, want untouched 2", got)
		}
		// Column-level spot check that the rebuild carried lat/lon.
		var lat float64
		if err := db.QueryRow(`SELECT latitude FROM activity_trackpoints WHERE activity_id = 'a1' AND sequence = 0`).Scan(&lat); err != nil {
			t.Fatalf("read a1 trackpoint: %v", err)
		}
		if lat != 40.1 {
			t.Errorf("a1 trackpoint latitude = %v, want 40.1", lat)
		}
	})

	t.Run("detail tables", func(t *testing.T) {
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_run_details`); got != 1 {
			t.Fatalf("activity_run_details rows = %d, want exactly a1", got)
		}
		var dist, raw float64
		var avgPace, bestPace, elev sql.NullFloat64
		var env string
		var route sql.NullString
		if err := db.QueryRow(`SELECT distance_meters, raw_distance_meters, avg_pace_sec_per_km,
				best_pace_sec_per_km, elevation_gain_meters, environment, route_geojson
			FROM activity_run_details WHERE activity_id = 'a1'`).
			Scan(&dist, &raw, &avgPace, &bestPace, &elev, &env, &route); err != nil {
			t.Fatalf("read run details: %v", err)
		}
		if dist != 10000 || raw != 10100 {
			t.Errorf("a1 distance/raw = %v/%v, want 10000/10100", dist, raw)
		}
		if !avgPace.Valid || avgPace.Float64 != 300 || !bestPace.Valid || bestPace.Float64 != 280 {
			t.Errorf("a1 paces = %v/%v, want 300/280", avgPace, bestPace)
		}
		if !elev.Valid || elev.Float64 != 42.5 {
			t.Errorf("a1 elevation = %v, want 42.5", elev)
		}
		if env != "outdoor" || !route.Valid || route.String != `{"type":"Feature"}` {
			t.Errorf("a1 environment/route = %v/%v", env, route)
		}

		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_walk_details WHERE activity_id = 'a2'`); got != 1 {
			t.Errorf("activity_walk_details a2 rows = %d, want 1", got)
		}
		if got := queryString(t, db, `SELECT environment FROM activity_walk_details WHERE activity_id = 'a2'`); got != "indoor" {
			t.Errorf("a2 environment = %q, want indoor", got)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_cycle_details`); got != 0 {
			t.Errorf("activity_cycle_details rows = %d, want 0", got)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_other_details`); got != 0 {
			t.Errorf("activity_other_details rows = %d, want 0", got)
		}
	})

	t.Run("strength children preserve integer ids", func(t *testing.T) {
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_exercises`); got != 2 {
			t.Fatalf("activity_exercises rows = %d, want 2", got)
		}
		if got := queryInt(t, db, `SELECT id FROM activity_exercises WHERE activity_id = 'w1' AND exercise_id = 'bench'`); got != 11 {
			t.Errorf("bench activity_exercise id = %d, want preserved 11", got)
		}
		var weNotes sql.NullString
		var weSuperset sql.NullInt64
		if err := db.QueryRow(`SELECT notes, superset_group FROM activity_exercises WHERE id = 11`).Scan(&weNotes, &weSuperset); err != nil {
			t.Fatalf("read activity_exercise 11: %v", err)
		}
		if !weNotes.Valid || weNotes.String != "we note" {
			t.Errorf("activity_exercise 11 notes = %v, want 'we note'", weNotes)
		}
		if !weSuperset.Valid || weSuperset.Int64 != 1 {
			t.Errorf("activity_exercise 11 superset_group = %v, want 1", weSuperset)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM sets`); got != 4 {
			t.Fatalf("sets rows = %d, want 4", got)
		}
		if got := queryInt(t, db, `SELECT activity_exercise_id FROM sets WHERE id = 102`); got != 11 {
			t.Errorf("set 102 activity_exercise_id = %d, want old workout_exercise_id 11", got)
		}
		if got := queryInt(t, db, `SELECT activity_exercise_id FROM sets WHERE id = 104`); got != 12 {
			t.Errorf("set 104 activity_exercise_id = %d, want old workout_exercise_id 12", got)
		}
	})

	t.Run("strength stat tables row-identical with activity_id", func(t *testing.T) {
		var prActivity string
		var prWeight float64
		if err := db.QueryRow(`SELECT activity_id, weight FROM personal_records WHERE id = 'pr1'`).Scan(&prActivity, &prWeight); err != nil {
			t.Fatalf("read pr1: %v", err)
		}
		if prActivity != "w1" || prWeight != 195 {
			t.Errorf("pr1 = (%s, %v), want (w1, 195)", prActivity, prWeight)
		}
		var preActivity string
		var prevWeight sql.NullFloat64
		if err := db.QueryRow(`SELECT activity_id, previous_weight FROM personal_record_events WHERE id = 'pre1'`).Scan(&preActivity, &prevWeight); err != nil {
			t.Fatalf("read pre1: %v", err)
		}
		if preActivity != "w1" || !prevWeight.Valid || prevWeight.Float64 != 185 {
			t.Errorf("pre1 = (%s, %v), want (w1, 185)", preActivity, prevWeight)
		}
		var ormActivity string
		var maxRM float64
		if err := db.QueryRow(`SELECT activity_id, max_estimated_1rm FROM exercise_one_rep_max_history WHERE id = 'orm1'`).Scan(&ormActivity, &maxRM); err != nil {
			t.Fatalf("read orm1: %v", err)
		}
		if ormActivity != "w1" || maxRM != 227.5 {
			t.Errorf("orm1 = (%s, %v), want (w1, 227.5)", ormActivity, maxRM)
		}
	})

	t.Run("agent memory rebuilt against activities", func(t *testing.T) {
		var text, src string
		if err := db.QueryRow(`SELECT distilled_text, source_workout_id FROM agent_memories WHERE id = 1`).Scan(&text, &src); err != nil {
			t.Fatalf("read agent memory: %v", err)
		}
		if src != "w1" || text != "left shoulder cranky on bench" {
			t.Errorf("agent memory = (%q, %q)", text, src)
		}
	})

	t.Run("timeline posts byte-identical", func(t *testing.T) {
		type post struct{ id, userID, srcType, srcID, occurredAt, visibility, createdAt, updatedAt string }
		want := map[string]post{
			"tp1": {"tp1", "u1", "workout", "w1", "2026-06-01T10:00:00Z", "private", "2026-06-01T11:05:00Z", "2026-06-01T11:05:00Z"},
			"tp2": {"tp2", "u1", "run", "a1", "2026-06-07T07:00:00Z", "private", "2026-06-07T08:00:00Z", "2026-06-07T08:00:00Z"},
		}
		rows, err := db.Query(`SELECT id, user_id, source_type, source_id, occurred_at, visibility, created_at, updated_at FROM timeline_post`)
		if err != nil {
			t.Fatalf("read timeline posts: %v", err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var p post
			if err := rows.Scan(&p.id, &p.userID, &p.srcType, &p.srcID, &p.occurredAt, &p.visibility, &p.createdAt, &p.updatedAt); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if p != want[p.id] {
				t.Errorf("timeline post %s = %+v, want %+v", p.id, p, want[p.id])
			}
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		if n != 2 {
			t.Errorf("timeline posts = %d, want 2", n)
		}
	})

	t.Run("planned workout kind normalized", func(t *testing.T) {
		if got := queryString(t, db, `SELECT completed_session_kind FROM planned_workouts WHERE id = 'pw1'`); got != "activity" {
			t.Errorf("completed_session_kind = %q, want activity", got)
		}
	})

	t.Run("schema integrity", func(t *testing.T) {
		for _, gone := range []string{"workouts", "workout_exercises", "activities_new", "sets_new"} {
			if tableExists(t, db, gone) {
				t.Errorf("table %s should be gone", gone)
			}
		}
		for _, idx := range []string{
			"idx_activities_dedup", "idx_activities_user_start", "idx_activities_user_type_start",
			"idx_activity_best_efforts_distance", "idx_pr_user_achieved", "idx_pre_activity",
			"idx_orm_history_user_exercise_time", "idx_agent_memories_source_workout",
		} {
			if !indexExists(t, db, idx) {
				t.Errorf("index %s missing after migration 042", idx)
			}
		}

		// The RENAME rewrote every rebuilt child's FK target to `activities`.
		fkTargets := func(child string) map[string]bool {
			t.Helper()
			rows, err := db.Query(`SELECT "table" FROM pragma_foreign_key_list(?)`, child)
			if err != nil {
				t.Fatalf("foreign_key_list(%s): %v", child, err)
			}
			defer rows.Close()
			targets := map[string]bool{}
			for rows.Next() {
				var target string
				if err := rows.Scan(&target); err != nil {
					t.Fatalf("scan fk target: %v", err)
				}
				targets[target] = true
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("foreign_key_list(%s) rows: %v", child, err)
			}
			return targets
		}
		for _, child := range []string{
			"activity_run_details", "activity_walk_details", "activity_cycle_details",
			"activity_other_details", "activity_exercises", "activity_trackpoints",
			"activity_best_efforts", "personal_records", "personal_record_events",
			"exercise_one_rep_max_history",
		} {
			targets := fkTargets(child)
			if targets["activities_new"] || targets["workouts"] || targets["workout_exercises"] {
				t.Errorf("%s still references an old/interim table: %v", child, targets)
			}
		}

		// No dangling references anywhere.
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

	t.Run("schema identical to a fresh database", func(t *testing.T) {
		// A DB migrated over live data and a DB built from scratch must end
		// at byte-identical DDL for every table 042 touches. Both are pinned at
		// 042 (like the enclosing test): later migrations that ALTER these
		// tables — e.g. 044's elevation triple — legitimately change their
		// stored DDL, so the comparison must be against 042's own end state.
		fresh := newEmptyDB(t)
		if err := migrateUpTo(fresh, 42); err != nil {
			t.Fatalf("migrate fresh through 042: %v", err)
		}
		tables := []string{
			"activities", "activity_trackpoints", "activity_best_efforts",
			"activity_run_details", "activity_walk_details", "activity_cycle_details",
			"activity_other_details", "activity_exercises", "sets",
			"personal_records", "personal_record_events",
			"exercise_one_rep_max_history", "agent_memories",
		}
		for _, table := range tables {
			migrated := queryString(t, db, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
			scratch := queryString(t, fresh, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
			if migrated != scratch {
				t.Errorf("DDL for %s diverges between migrated and fresh DBs:\nmigrated: %s\nfresh:    %s", table, migrated, scratch)
			}
		}
	})

	t.Run("cascade through new base", func(t *testing.T) {
		// A hard delete of the folded workout row takes its exercises, sets,
		// trackpoints, and stat rows with it — proving every rebuilt FK
		// actually resolves against the renamed base table.
		if _, err := db.Exec(`DELETE FROM activities WHERE id = 'w3'`); err != nil {
			t.Fatalf("hard delete w3: %v", err)
		}
		if got := queryInt(t, db, `SELECT COUNT(*) FROM activity_trackpoints WHERE activity_id = 'w3'`); got != 0 {
			t.Errorf("w3 trackpoints after delete = %d, want cascaded 0", got)
		}
	})
}

// TestMigrate042_IdCollisionAborts proves transactionality: a workout id that
// collides with an existing activity id fails the plain INSERT, the migration
// errors, and the old tables are still present afterward.
func TestMigrate042_IdCollisionAborts(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	if err := migrateUpTo(db, 41); err != nil {
		t.Fatalf("migrate up to 041: %v", err)
	}
	seedUser(t, db, "u1")
	seedActivityPre042(t, db, "dup", "u1", "src-dup")
	if _, err := db.Exec(`INSERT INTO workouts (id, user_id, performed_at, created_at, updated_at)
		VALUES ('dup', 'u1', '2026-06-01T10:00:00Z', '2026-06-01T10:00:00Z', '2026-06-01T10:00:00Z')`); err != nil {
		t.Fatalf("seed colliding workout: %v", err)
	}

	if err := Migrate(db); err == nil {
		t.Fatal("Migrate should error on a workout/activity id collision")
	}

	// The whole 042 transaction rolled back: old tables still exist and the
	// version was not recorded.
	for _, table := range []string{"workouts", "workout_exercises", "activities"} {
		if !tableExists(t, db, table) {
			t.Errorf("table %s should still exist after aborted migration", table)
		}
	}
	if tableExists(t, db, "activity_run_details") {
		t.Error("activity_run_details should not exist after aborted migration")
	}
	if got := queryInt(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version = 42`); got != 0 {
		t.Error("version 42 should not be recorded after aborted migration")
	}
}
