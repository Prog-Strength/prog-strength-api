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
