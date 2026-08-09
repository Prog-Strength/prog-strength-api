package weather

import (
	"database/sql"
	"testing"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// mustInsertActivity inserts a live activity so activity_weather rows have a
// parent to reference. Mirrors the seed shape used by the other packages'
// activity tests.
func mustInsertActivity(t *testing.T, db *sql.DB, activityID, userID string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO activities (id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, duration_seconds, tcx_s3_key, created_at, updated_at)
		VALUES (?, ?, 'running', 'manual_tcx', ?, '2026-08-09T14:00:00Z', 1800, '', '2026-08-09T15:00:00Z', '2026-08-09T15:00:00Z')
	`, activityID, userID, activityID)
	if err != nil {
		t.Fatalf("insert activity %q: %v", activityID, err)
	}
}

func TestMigration057ActivityWeatherTableExists(t *testing.T) {
	database := dbtest.New(t)

	var name string
	err := database.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'activity_weather'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("activity_weather table missing after migration: %v", err)
	}
}

// The status set is closed in SQL as well as in Go: a typo'd status is the
// value that would make the backfill re-spend budget forever, so the database
// refuses it rather than storing an unreadable disposition.
func TestMigration057ActivityWeatherStatusCheckRejectsUnknown(t *testing.T) {
	database := dbtest.New(t)
	mustInsertUser(t, database, "u1")
	mustInsertActivity(t, database, "a1", "u1")

	_, err := database.Exec(`
		INSERT INTO activity_weather (activity_id, status, fetched_at)
		VALUES ('a1', 'definitely_not_a_status', '2026-08-09T15:00:00Z')
	`)
	if err == nil {
		t.Fatal("insert with an unknown status succeeded, want CHECK constraint failure")
	}

	for _, status := range []string{"ok", "no_coordinates", "unavailable"} {
		mustInsertActivity(t, database, "a-"+status, "u1")
		if _, err := database.Exec(`
			INSERT INTO activity_weather (activity_id, status, fetched_at)
			VALUES (?, ?, '2026-08-09T15:00:00Z')
		`, "a-"+status, status); err != nil {
			t.Errorf("insert with status %q: %v", status, err)
		}
	}
}

// dbtest opens with _foreign_keys=on, so this exercises the real cascade a
// deleted activity would take in production.
func TestMigration057ActivityWeatherCascadesFromActivity(t *testing.T) {
	database := dbtest.New(t)
	mustInsertUser(t, database, "u1")
	mustInsertActivity(t, database, "a1", "u1")

	if _, err := database.Exec(`
		INSERT INTO activity_weather (activity_id, status, lat, lon, observed_at, temp_c, fetched_at)
		VALUES ('a1', 'ok', 39.7392, -104.9847, '2026-08-09T14:00:00Z', 31.8, '2026-08-09T15:00:00Z')
	`); err != nil {
		t.Fatalf("insert activity_weather: %v", err)
	}

	if _, err := database.Exec(`DELETE FROM activities WHERE id = 'a1'`); err != nil {
		t.Fatalf("delete activity: %v", err)
	}

	var count int
	if err := database.QueryRow(`SELECT count(*) FROM activity_weather WHERE activity_id = 'a1'`).Scan(&count); err != nil {
		t.Fatalf("count activity_weather: %v", err)
	}
	if count != 0 {
		t.Errorf("activity_weather rows after parent delete = %d, want 0 (ON DELETE CASCADE)", count)
	}
}
