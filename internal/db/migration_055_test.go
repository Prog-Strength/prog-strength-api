package db

import (
	"database/sql"
	"encoding/json"
	"testing"
)

// section mirrors dashboard.Section for assertions here. internal/db must not
// import internal/dashboard (the dependency runs the other way), so the shape
// is restated rather than shared.
type section struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Collapsed bool     `json:"collapsed"`
	TileIDs   []string `json:"tile_ids"`
}

// seed054Layouts builds the pre-055 world at schema 054: three layout rows in
// the flat tile_ids shape, including an empty one (a legitimate "bare
// dashboard" preference) and one holding a since-unknown id, which the wrap
// must carry through untouched — filtering is the Go read path's job, not the
// migration's.
func seed054Layouts(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrateUpTo(db, 54); err != nil {
		t.Fatalf("migrate up to 054: %v", err)
	}
	for _, u := range []string{"u1", "u2", "u3"} {
		seedUser(t, db, u)
	}
	for _, r := range []struct{ user, tileIDs string }{
		{"u1", `["running","lifting","steps","nutrition"]`},
		{"u2", `[]`},
		{"u3", `["running","retired_tile"]`},
	} {
		if _, err := db.Exec(`
			INSERT INTO user_dashboard_layouts (user_id, tile_ids, updated_at)
			VALUES (?, ?, '2026-08-01T00:00:00Z')
		`, r.user, r.tileIDs); err != nil {
			t.Fatalf("seed layout %s: %v", r.user, err)
		}
	}
}

func sectionsFor(t *testing.T, db *sql.DB, userID string) []section {
	t.Helper()
	raw := queryString(t, db, `SELECT sections FROM user_dashboard_layouts WHERE user_id = ?`, userID)
	var out []section
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode sections for %s: %v (raw=%s)", userID, err, raw)
	}
	return out
}

func TestMigrate055_DashboardLayoutSections(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	seed054Layouts(t, db)
	if err := migrateUpTo(db, 55); err != nil {
		t.Fatalf("migrate through 055: %v", err)
	}

	t.Run("each row wraps into exactly one untitled section", func(t *testing.T) {
		for _, u := range []string{"u1", "u2", "u3"} {
			got := sectionsFor(t, db, u)
			if len(got) != 1 {
				t.Errorf("%s: sections = %d (%+v), want 1", u, len(got), got)
				continue
			}
			// Untitled is what makes the migration invisible: an untitled
			// section renders as a bare grid, no header and no rule.
			if got[0].Title != "" {
				t.Errorf("%s: title = %q, want empty", u, got[0].Title)
			}
			if got[0].Collapsed {
				t.Errorf("%s: collapsed = true, want false", u)
			}
			if got[0].ID == "" {
				t.Errorf("%s: section id is empty", u)
			}
		}
	})

	t.Run("tile ids survive in order", func(t *testing.T) {
		got := sectionsFor(t, db, "u1")[0].TileIDs
		want := []string{"running", "lifting", "steps", "nutrition"}
		if len(got) != len(want) {
			t.Fatalf("tile ids = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("tile ids = %v, want %v", got, want)
			}
		}
	})

	t.Run("an empty layout wraps into one empty section", func(t *testing.T) {
		got := sectionsFor(t, db, "u2")
		if len(got) != 1 || len(got[0].TileIDs) != 0 {
			t.Errorf("sections = %+v, want one section with no tiles", got)
		}
	})

	t.Run("an unknown tile id is carried, not filtered", func(t *testing.T) {
		// Filtering belongs to the Go read path (dashboard.Normalize), which
		// keeps this migration lossless and the catalog the single source of
		// truth for the closed set.
		got := sectionsFor(t, db, "u3")[0].TileIDs
		if len(got) != 2 || got[1] != "retired_tile" {
			t.Errorf("tile ids = %v, want the unknown id carried through", got)
		}
	})

	t.Run("the tile_ids column is gone", func(t *testing.T) {
		if columnExists(t, db, "user_dashboard_layouts", "tile_ids") {
			t.Error("tile_ids column still present after 055")
		}
	})

	t.Run("the wrap is reversible", func(t *testing.T) {
		// Unwrapping the first section's tile_ids reconstructs the dropped
		// column exactly — the rollback story in the SOW.
		raw := queryString(t, db,
			`SELECT json_extract(sections, '$[0].tile_ids') FROM user_dashboard_layouts WHERE user_id = 'u1'`)
		if raw != `["running","lifting","steps","nutrition"]` {
			t.Errorf("unwrapped tile_ids = %s, want the original array", raw)
		}
	})
}

// TestMigrate055_NoRows confirms the migration is a no-op on an empty table —
// the common case, since a row only appears once a user customizes.
func TestMigrate055_NoRows(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	if err := migrateUpTo(db, 54); err != nil {
		t.Fatalf("migrate up to 054: %v", err)
	}
	if err := migrateUpTo(db, 55); err != nil {
		t.Fatalf("migrate through 055 on an empty table: %v", err)
	}
	if got := queryInt(t, db, `SELECT COUNT(*) FROM user_dashboard_layouts`); got != 0 {
		t.Errorf("rows = %d, want 0", got)
	}
}
