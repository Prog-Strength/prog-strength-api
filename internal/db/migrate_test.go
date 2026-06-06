package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newMigratedDB opens a fresh database file in a t.TempDir(), runs all
// migrations, and returns the handle. Each test gets its own file so
// they can run in parallel without sharing schema state. Foreign keys
// are on to exercise the same constraints as production.
func newMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn
}

// TestMigrate012_CustomMealNameXOR exercises migration 012's three-way
// XOR CHECK on nutrition_log_entries: pantry-backed and recipe-backed
// rows still insert, a custom_meal_name-only row now inserts, and rows
// with zero or two sources are rejected by the recreated CHECK.
func TestMigrate012_CustomMealNameXOR(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	// Seed the referenced pantry item + recipe so the FK references in
	// the log rows resolve.
	if _, err := db.Exec(`
		INSERT INTO pantry_items (
			id, user_id, name, calories, protein_g, fat_g, carbs_g,
			serving_size, serving_unit, created_at, updated_at
		) VALUES ('pi1', 'u1', 'Egg', 70, 6, 5, 0.5, 1, 'egg', '2026-06-06', '2026-06-06')
	`); err != nil {
		t.Fatalf("seed pantry item: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recipes (id, user_id, name, created_at, updated_at)
		VALUES ('re1', 'u1', 'Omelette', '2026-06-06', '2026-06-06')
	`); err != nil {
		t.Fatalf("seed recipe: %v", err)
	}

	insertLog := func(id string, pantryItem, recipe, custom any) error {
		_, err := db.Exec(`
			INSERT INTO nutrition_log_entries (
				id, user_id, consumed_at,
				pantry_item_id, recipe_id, custom_meal_name,
				quantity, calories, protein_g, fat_g, carbs_g,
				created_at, meal
			) VALUES (?, 'u1', '2026-06-06', ?, ?, ?, 1, 100, 10, 5, 12, '2026-06-06', 'lunch')
		`, id, pantryItem, recipe, custom)
		return err
	}

	// 3 pantry-backed + 3 recipe-backed rows all succeed.
	for i, id := range []string{"p1", "p2", "p3"} {
		if err := insertLog(id, "pi1", nil, nil); err != nil {
			t.Fatalf("pantry-backed insert %d: %v", i, err)
		}
	}
	for i, id := range []string{"r1", "r2", "r3"} {
		if err := insertLog(id, nil, "re1", nil); err != nil {
			t.Fatalf("recipe-backed insert %d: %v", i, err)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM nutrition_log_entries`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 6 {
		t.Fatalf("want 6 rows, got %d", count)
	}

	// A custom_meal_name-only row now inserts.
	if err := insertLog("c1", nil, nil, "Chipotle chicken bowl"); err != nil {
		t.Fatalf("custom-only insert should succeed: %v", err)
	}

	// Zero sources fails the CHECK.
	if err := insertLog("z1", nil, nil, nil); err == nil {
		t.Error("zero-source insert should fail the CHECK, got nil error")
	}

	// Two sources (pantry + recipe) fails the CHECK.
	if err := insertLog("two1", "pi1", "re1", nil); err == nil {
		t.Error("two-source insert should fail the CHECK, got nil error")
	}
}

// seedUser inserts a minimal user row so running_sessions / distance_unit
// tests have an owner to reference (user_id is not a FK, but the
// distance_unit default and CHECK live on this table).
func seedUser(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO users (id, email, display_name, weight_unit, created_at, updated_at)
		VALUES (?, ?, 'Runner', 'lb', '2026-06-06', '2026-06-06')
	`, id, id+"@example.com"); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

// seedSession inserts a running_sessions row with the NOT NULL fields set.
func seedSession(t *testing.T, db *sql.DB, id, userID, garminID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO running_sessions (
			id, user_id, garmin_activity_id, start_time,
			distance_meters, duration_seconds, avg_pace_sec_per_km,
			tcx_s3_key, created_at
		) VALUES (?, ?, ?, '2026-06-06T07:00:00Z', 10000, 3000, 300, ?, '2026-06-06T07:30:00Z')
	`, id, userID, garminID, "runs/"+userID+"/"+id+".tcx"); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// TestMigrate013_RunningTablesExist exercises a full happy-path write:
// a user, one running session, and two trackpoints all insert and read back.
func TestMigrate013_RunningTablesExist(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedSession(t, db, "s1", "u1", "garmin-1")

	for seq, elapsed := range []int{0, 10} {
		if _, err := db.Exec(`
			INSERT INTO running_trackpoints (
				session_id, sequence, elapsed_seconds, distance_meters,
				heart_rate_bpm, pace_sec_per_km, elevation_meters
			) VALUES ('s1', ?, ?, ?, 150, 300, 12.5)
		`, seq, elapsed, elapsed*3); err != nil {
			t.Fatalf("insert trackpoint %d: %v", seq, err)
		}
	}

	var sessionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM running_sessions WHERE user_id = 'u1'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("want 1 session, got %d", sessionCount)
	}

	var pointCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM running_trackpoints WHERE session_id = 's1'`).Scan(&pointCount); err != nil {
		t.Fatalf("count trackpoints: %v", err)
	}
	if pointCount != 2 {
		t.Fatalf("want 2 trackpoints, got %d", pointCount)
	}
}

// TestMigrate013_UniqueGarminActivity verifies the dedup constraint: a
// second session with the same (user_id, garmin_activity_id) is rejected,
// while the same garmin_activity_id under a different user is allowed.
func TestMigrate013_UniqueGarminActivity(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedSession(t, db, "s1", "u1", "garmin-dup")

	// Same (user_id, garmin_activity_id) violates UNIQUE.
	if _, err := db.Exec(`
		INSERT INTO running_sessions (
			id, user_id, garmin_activity_id, start_time,
			distance_meters, duration_seconds, avg_pace_sec_per_km,
			tcx_s3_key, created_at
		) VALUES ('s2', 'u1', 'garmin-dup', '2026-06-06T07:00:00Z', 10000, 3000, 300, 'k', '2026-06-06T07:30:00Z')
	`); err == nil {
		t.Fatal("duplicate (user_id, garmin_activity_id) insert should fail UNIQUE, got nil error")
	}

	// Same garmin_activity_id for a different user is fine.
	seedUser(t, db, "u2")
	seedSession(t, db, "s3", "u2", "garmin-dup")
}

// TestMigrate013_TrackpointCascade verifies ON DELETE CASCADE: deleting a
// session removes its trackpoints. newMigratedDB opens with _foreign_keys=on.
func TestMigrate013_TrackpointCascade(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedSession(t, db, "s1", "u1", "garmin-1")
	if _, err := db.Exec(`
		INSERT INTO running_trackpoints (session_id, sequence, elapsed_seconds, distance_meters)
		VALUES ('s1', 0, 0, 0), ('s1', 1, 10, 30)
	`); err != nil {
		t.Fatalf("insert trackpoints: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM running_sessions WHERE id = 's1'`); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	var pointCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM running_trackpoints WHERE session_id = 's1'`).Scan(&pointCount); err != nil {
		t.Fatalf("count trackpoints after delete: %v", err)
	}
	if pointCount != 0 {
		t.Fatalf("want trackpoints cascaded to 0, got %d", pointCount)
	}
}

// TestMigrate013_UsersDistanceUnitDefault verifies the new users column:
// rows inserted without distance_unit backfill to 'mi', and an out-of-set
// value is rejected by the CHECK.
func TestMigrate013_UsersDistanceUnitDefault(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	// Inserting without distance_unit takes the DEFAULT.
	seedUser(t, db, "u1")
	var unit string
	if err := db.QueryRow(`SELECT distance_unit FROM users WHERE id = 'u1'`).Scan(&unit); err != nil {
		t.Fatalf("select distance_unit: %v", err)
	}
	if unit != "mi" {
		t.Fatalf("want default distance_unit 'mi', got %q", unit)
	}

	// 'km' is a valid explicit value.
	if _, err := db.Exec(`
		INSERT INTO users (id, email, display_name, weight_unit, distance_unit, created_at, updated_at)
		VALUES ('u2', 'u2@example.com', 'Runner', 'kg', 'km', '2026-06-06', '2026-06-06')
	`); err != nil {
		t.Fatalf("insert with distance_unit 'km' should succeed: %v", err)
	}

	// An out-of-set value violates the CHECK.
	if _, err := db.Exec(`
		INSERT INTO users (id, email, display_name, weight_unit, distance_unit, created_at, updated_at)
		VALUES ('u3', 'u3@example.com', 'Runner', 'kg', 'furlongs', '2026-06-06', '2026-06-06')
	`); err == nil {
		t.Fatal("invalid distance_unit should fail the CHECK, got nil error")
	}
}
