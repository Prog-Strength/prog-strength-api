package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newEmptyDB opens a fresh database file in a t.TempDir() using the same DSN as
// newMigratedDB but without running any migrations, so tests can drive
// migrateWith directly with synthetic Go migrations.
func newEmptyDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestGoMigration_AppliesOnceAndRecordsVersion verifies a Go migration runs in
// its transaction, records its version in the shared ledger, and is skipped on
// a subsequent run.
func TestGoMigration_AppliesOnceAndRecordsVersion(t *testing.T) {
	t.Parallel()
	conn := newEmptyDB(t)

	runs := 0
	gm := goMigration{
		Version: 9001,
		Name:    "marker",
		Run: func(ctx context.Context, tx *sql.Tx) error {
			runs++
			_, err := tx.ExecContext(ctx, `CREATE TABLE go_marker (id INTEGER PRIMARY KEY)`)
			return err
		},
	}

	if err := migrateWith(conn, []goMigration{gm}); err != nil {
		t.Fatalf("first migrateWith: %v", err)
	}
	if runs != 1 {
		t.Fatalf("want runs == 1 after first run, got %d", runs)
	}

	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 9001`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("want version 9001 recorded once, got count %d", count)
	}

	// The Go migration's table exists.
	if _, err := conn.Exec(`INSERT INTO go_marker (id) VALUES (1)`); err != nil {
		t.Fatalf("go migration table should exist: %v", err)
	}

	// Re-running skips the already-applied migration.
	if err := migrateWith(conn, []goMigration{gm}); err != nil {
		t.Fatalf("second migrateWith: %v", err)
	}
	if runs != 1 {
		t.Fatalf("want runs == 1 after re-run (skipped), got %d", runs)
	}
}

// TestGoMigration_RunsInVersionOrderWithSQL verifies the runner sorts Go
// migrations by version regardless of registration order.
func TestGoMigration_RunsInVersionOrderWithSQL(t *testing.T) {
	t.Parallel()
	conn := newEmptyDB(t)

	var order []int
	record := func(v int) func(ctx context.Context, tx *sql.Tx) error {
		return func(ctx context.Context, tx *sql.Tx) error {
			order = append(order, v)
			return nil
		}
	}

	migs := []goMigration{
		{Version: 9003, Name: "third", Run: record(9003)},
		{Version: 9002, Name: "second", Run: record(9002)},
	}
	if err := migrateWith(conn, migs); err != nil {
		t.Fatalf("migrateWith: %v", err)
	}

	if len(order) != 2 || order[0] != 9002 || order[1] != 9003 {
		t.Fatalf("want order [9002 9003], got %v", order)
	}
}

// TestCollectMigrations_RejectsDuplicateVersion verifies a Go migration whose
// version collides with an existing SQL migration is rejected.
func TestCollectMigrations_RejectsDuplicateVersion(t *testing.T) {
	t.Parallel()

	_, err := collectMigrations([]goMigration{{
		Version: 27,
		Name:    "dup",
		Run:     func(ctx context.Context, tx *sql.Tx) error { return nil },
	}})
	if err == nil {
		t.Fatal("want error for duplicate version 27 (already a SQL migration), got nil")
	}
}

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

// seedUser inserts a minimal user row so activities / distance_unit
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

// seedActivity inserts an activities row with the NOT NULL fields set.
// Pinned to manual_tcx + running so the post-015 schema is exercised on
// the path the running domain still produces.
func seedActivity(t *testing.T, db *sql.DB, id, userID, sourceActivityID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds, avg_pace_sec_per_km,
			tcx_s3_key, created_at
		) VALUES (?, ?, 'running', 'manual_tcx', ?, '2026-06-06T07:00:00Z', 10000, 3000, 300, ?, '2026-06-06T07:30:00Z')
	`, id, userID, sourceActivityID, "user_id="+userID+"/activity_type=running/year=2026/month=06/day=06/"+id+".tcx"); err != nil {
		t.Fatalf("seed activity %s: %v", id, err)
	}
}

// TestActivities_TablesExist exercises a full happy-path write against the
// post-015 schema: a user, one activity, and two trackpoints all insert
// and read back.
func TestActivities_TablesExist(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedActivity(t, db, "s1", "u1", "garmin-1")

	for seq, elapsed := range []int{0, 10} {
		if _, err := db.Exec(`
			INSERT INTO activity_trackpoints (
				activity_id, sequence, elapsed_seconds, distance_meters,
				heart_rate_bpm, pace_sec_per_km, elevation_meters
			) VALUES ('s1', ?, ?, ?, 150, 300, 12.5)
		`, seq, elapsed, elapsed*3); err != nil {
			t.Fatalf("insert trackpoint %d: %v", seq, err)
		}
	}

	var activityCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activities WHERE user_id = 'u1'`).Scan(&activityCount); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	if activityCount != 1 {
		t.Fatalf("want 1 activity, got %d", activityCount)
	}

	var pointCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activity_trackpoints WHERE activity_id = 's1'`).Scan(&pointCount); err != nil {
		t.Fatalf("count trackpoints: %v", err)
	}
	if pointCount != 2 {
		t.Fatalf("want 2 trackpoints, got %d", pointCount)
	}
}

// TestActivities_UniqueSourceActivity verifies the post-015 dedup
// constraint: a second activity with the same
// (user_id, ingest_source, source_activity_id) is rejected, while the
// same source_activity_id under a different user or a different ingest
// source is allowed.
func TestActivities_UniqueSourceActivity(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedActivity(t, db, "s1", "u1", "src-dup")

	// Same (user_id, ingest_source, source_activity_id) violates UNIQUE.
	if _, err := db.Exec(`
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds, avg_pace_sec_per_km,
			tcx_s3_key, created_at
		) VALUES ('s2', 'u1', 'running', 'manual_tcx', 'src-dup',
		          '2026-06-06T07:00:00Z', 10000, 3000, 300, 'k', '2026-06-06T07:30:00Z')
	`); err == nil {
		t.Fatal("duplicate (user_id, ingest_source, source_activity_id) insert should fail UNIQUE, got nil error")
	}

	// Same source_activity_id for a different user is fine.
	seedUser(t, db, "u2")
	seedActivity(t, db, "s3", "u2", "src-dup")

	// Same source_activity_id from a different ingest source is fine
	// (a future Garmin Connect sync of a run already uploaded via TCX is
	// a separate record by design).
	if _, err := db.Exec(`
		INSERT INTO activities (
			id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, distance_meters, duration_seconds, avg_pace_sec_per_km,
			tcx_s3_key, created_at
		) VALUES ('s4', 'u1', 'running', 'garmin_api', 'src-dup',
		          '2026-06-06T08:00:00Z', 10000, 3000, 300, 'k4', '2026-06-06T08:30:00Z')
	`); err != nil {
		t.Fatalf("cross-source same activity id should succeed: %v", err)
	}
}

// TestActivities_TrackpointCascade verifies ON DELETE CASCADE: deleting
// an activity removes its trackpoints. newMigratedDB opens with
// _foreign_keys=on.
func TestActivities_TrackpointCascade(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedActivity(t, db, "s1", "u1", "garmin-1")
	if _, err := db.Exec(`
		INSERT INTO activity_trackpoints (activity_id, sequence, elapsed_seconds, distance_meters)
		VALUES ('s1', 0, 0, 0), ('s1', 1, 10, 30)
	`); err != nil {
		t.Fatalf("insert trackpoints: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM activities WHERE id = 's1'`); err != nil {
		t.Fatalf("delete activity: %v", err)
	}

	var pointCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activity_trackpoints WHERE activity_id = 's1'`).Scan(&pointCount); err != nil {
		t.Fatalf("count trackpoints after delete: %v", err)
	}
	if pointCount != 0 {
		t.Fatalf("want trackpoints cascaded to 0, got %d", pointCount)
	}
}

// TestMigrate016_ActivityBestEfforts verifies migration 016 layers cleanly
// on the fully-migrated schema: the table + index exist, the distance_key
// CHECK rejects an out-of-set value, and the FK cascade clears best-effort
// rows on a hard activity delete.
func TestMigrate016_ActivityBestEfforts(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	seedUser(t, db, "u1")
	seedActivity(t, db, "s1", "u1", "src-1")

	// The table exists and accepts a valid distance_key.
	if _, err := db.Exec(`
		INSERT INTO activity_best_efforts (activity_id, distance_key, duration_seconds)
		VALUES ('s1', '5k', 1184.7)
	`); err != nil {
		t.Fatalf("insert valid best effort: %v", err)
	}

	// The supporting index exists.
	var idxName string
	if err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_activity_best_efforts_distance'
	`).Scan(&idxName); err != nil {
		t.Fatalf("expected idx_activity_best_efforts_distance to exist: %v", err)
	}

	// An out-of-set distance_key violates the CHECK.
	if _, err := db.Exec(`
		INSERT INTO activity_best_efforts (activity_id, distance_key, duration_seconds)
		VALUES ('s1', '15k', 3600)
	`); err == nil {
		t.Error("invalid distance_key should fail the CHECK, got nil error")
	}

	// FK cascade: a hard delete of the activity removes its best-effort rows.
	if _, err := db.Exec(`DELETE FROM activities WHERE id = 's1'`); err != nil {
		t.Fatalf("delete activity: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activity_best_efforts WHERE activity_id = 's1'`).Scan(&count); err != nil {
		t.Fatalf("count best efforts after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("want best efforts cascaded to 0, got %d", count)
	}
}

// TestMigrate039And040_WhoopTablesExist verifies the Whoop migrations layer
// cleanly on the fully-migrated schema: both tables exist, a valid connection
// and recovery row insert, and the status / (user_id, date) constraints hold.
func TestMigrate039And040_WhoopTablesExist(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	for _, table := range []string{"user_whoop_connection", "user_whoop_recovery"} {
		var name string
		if err := db.QueryRow(`
			SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?
		`, table).Scan(&name); err != nil {
			t.Fatalf("expected table %s to exist: %v", table, err)
		}
	}

	// A connection row inserts, and an out-of-set status violates the CHECK.
	if _, err := db.Exec(`
		INSERT INTO user_whoop_connection (
			user_id, whoop_user_id, access_token_enc, access_token_nonce,
			refresh_token_enc, refresh_token_nonce, token_expires_at, scopes,
			status, connected_at, updated_at
		) VALUES ('u1', 42, x'01', x'02', x'03', x'04', '2026-07-22T00:00:00Z',
		          'read:recovery', 'connected', '2026-07-22', '2026-07-22')
	`); err != nil {
		t.Fatalf("insert whoop connection: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_whoop_connection (
			user_id, whoop_user_id, access_token_enc, access_token_nonce,
			refresh_token_enc, refresh_token_nonce, token_expires_at, scopes,
			status, connected_at, updated_at
		) VALUES ('u2', 43, x'01', x'02', x'03', x'04', '2026-07-22T00:00:00Z',
		          'read:recovery', 'bogus', '2026-07-22', '2026-07-22')
	`); err == nil {
		t.Error("invalid status should fail the CHECK, got nil error")
	}

	// A recovery row inserts, and a duplicate (user_id, date) violates UNIQUE.
	if _, err := db.Exec(`
		INSERT INTO user_whoop_recovery (
			id, user_id, date, recovery_score, resting_heart_rate,
			hrv_rmssd_milli, cycle_id, sleep_id, created_at, updated_at
		) VALUES ('r1', 'u1', '2026-07-22', 88.0, 52.0, 65.5, 100, 's1',
		          '2026-07-22', '2026-07-22')
	`); err != nil {
		t.Fatalf("insert whoop recovery: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_whoop_recovery (
			id, user_id, date, recovery_score, resting_heart_rate,
			hrv_rmssd_milli, cycle_id, sleep_id, created_at, updated_at
		) VALUES ('r2', 'u1', '2026-07-22', 90.0, 50.0, 70.0, 101, 's2',
		          '2026-07-22', '2026-07-22')
	`); err == nil {
		t.Error("duplicate (user_id, date) should fail UNIQUE, got nil error")
	}

	// The supporting indexes exist.
	for _, idx := range []string{
		"idx_user_whoop_recovery_user_date",
		"idx_user_whoop_recovery_sleep",
	} {
		var name string
		if err := db.QueryRow(`
			SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?
		`, idx).Scan(&name); err != nil {
			t.Fatalf("expected index %s to exist: %v", idx, err)
		}
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
