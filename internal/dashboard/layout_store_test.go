package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// mustInsertUser inserts a minimal live user row so layouts can satisfy the
// user_id foreign key (and so the cascade test has a parent to delete).
func mustInsertUser(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO users (id, email, display_name, weight_unit, created_at, updated_at)
		VALUES (?, ?, ?, 'lb', '2026-01-01', '2026-01-01')
	`, userID, userID+"@example.com", userID)
	if err != nil {
		t.Fatalf("insert user %q: %v", userID, err)
	}
}

// assertTileIDs fails unless got equals want in both length and order.
func assertTileIDs(t *testing.T, got []TileID, want []TileID) {
	t.Helper()
	if got == nil {
		t.Fatalf("tile ids: got nil slice, want %v (non-nil)", want)
	}
	if len(got) != len(want) {
		t.Fatalf("tile ids: got %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tile ids: at index %d got %q, want %q (got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

// assertSections fails unless got matches want section-for-section on id,
// title, collapsed, and tile order.
func assertSections(t *testing.T, got, want []Section) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("sections: got %d (%+v), want %d (%+v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Errorf("section %d id: got %q, want %q", i, got[i].ID, want[i].ID)
		}
		if got[i].Title != want[i].Title {
			t.Errorf("section %d title: got %q, want %q", i, got[i].Title, want[i].Title)
		}
		if got[i].Collapsed != want[i].Collapsed {
			t.Errorf("section %d collapsed: got %v, want %v", i, got[i].Collapsed, want[i].Collapsed)
		}
		assertTileIDs(t, got[i].TileIDs, want[i].TileIDs)
	}
}

func TestLayout_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	want := []Section{
		{ID: "a", Title: "Endurance", TileIDs: []TileID{TileRunning, TileSteps}},
		{ID: "b", Title: "Strength", Collapsed: true, TileIDs: []TileID{TileLifting}},
	}
	if err := repo.Upsert(ctx, "u1", want); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertSections(t, got.Sections, want)
}

func TestLayout_UpsertOverwrites(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	if err := repo.Upsert(ctx, "u1", SingleSection([]TileID{TileRunning, TileSteps})); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	want := []Section{{ID: "s1", Title: "Only", TileIDs: []TileID{TileNutrition, TileStreak}}}
	if err := repo.Upsert(ctx, "u1", want); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertSections(t, got.Sections, want)
}

// TestLayout_FiltersUnknownIDs seeds a raw row containing a retired/unknown id
// and confirms Get drops it while preserving the order of the valid ids. The
// blob carries no CHECK (migration 055), so this filtering is the only thing
// keeping a retired tile from reaching the response.
func TestLayout_FiltersUnknownIDs(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	_, err := db.Exec(
		`INSERT INTO user_dashboard_layouts (user_id, sections, updated_at) VALUES (?, ?, ?)`,
		"u1", `[{"id":"a","title":"","collapsed":false,"tile_ids":["running","bogus_tile","steps"]}]`,
		"2026-01-01",
	)
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertTileIDs(t, got.Sections[0].TileIDs, []TileID{TileRunning, TileSteps})
}

// TestLayout_ReadNormalizesStoredBlob covers the repairs a stored blob can need
// that the write path would have rejected: the same tile in two sections, a
// duplicated section id, and an untrimmed title. The read path must repair
// rather than error — a layout accepted under an older rule set can never be
// allowed to blank the dashboard.
func TestLayout_ReadNormalizesStoredBlob(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	_, err := db.Exec(
		`INSERT INTO user_dashboard_layouts (user_id, sections, updated_at) VALUES (?, ?, ?)`,
		"u1", `[
			{"id":"a","title":"  Endurance  ","collapsed":false,"tile_ids":["running","steps"]},
			{"id":"a","title":"Strength","collapsed":false,"tile_ids":["running","lifting"]}
		]`, "2026-01-01",
	)
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Sections) != 2 {
		t.Fatalf("sections: got %d, want 2", len(got.Sections))
	}
	if got.Sections[0].Title != "Endurance" {
		t.Errorf("title: got %q, want trimmed %q", got.Sections[0].Title, "Endurance")
	}
	if got.Sections[1].ID == got.Sections[0].ID {
		t.Errorf("duplicate section id survived normalization: both %q", got.Sections[0].ID)
	}
	// running was claimed by the first section; the second keeps only lifting.
	assertTileIDs(t, got.Sections[1].TileIDs, []TileID{TileLifting})
}

func TestLayout_GetNotFound(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteLayoutRepository(db)

	_, err := repo.Get(ctx, "nobody")
	if !errors.Is(err, ErrLayoutNotFound) {
		t.Fatalf("get: got %v, want ErrLayoutNotFound", err)
	}
}

// TestLayout_EmptyRoundTrips checks an empty layout persists and reads back as
// exactly one untitled empty section — the floor Normalize enforces so no
// caller ever handles a sectionless layout. It stays distinct from "no row"
// (which returns ErrLayoutNotFound and resolves the default).
func TestLayout_EmptyRoundTrips(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	if err := repo.Upsert(ctx, "u1", nil); err != nil {
		t.Fatalf("upsert nil: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertSections(t, got.Sections, []Section{{ID: "s1", TileIDs: []TileID{}}})
}

// TestLayout_CascadeOnUserDelete confirms the ON DELETE CASCADE fires: deleting
// the parent user removes the layout row. dbtest opens with _foreign_keys=on
// (see db.Open), so the FK is enforced without an extra PRAGMA here.
func TestLayout_CascadeOnUserDelete(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	if err := repo.Upsert(ctx, "u1", SingleSection([]TileID{TileRunning})); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, "u1"); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	_, err := repo.Get(ctx, "u1")
	if !errors.Is(err, ErrLayoutNotFound) {
		t.Fatalf("get after user delete: got %v, want ErrLayoutNotFound", err)
	}
}
