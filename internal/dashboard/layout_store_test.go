package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
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

func TestLayout_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	want := []TileID{TileRunning, TileSteps, TileLifting}
	if err := repo.Upsert(ctx, "u1", want); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertTileIDs(t, got.TileIDs, want)
}

func TestLayout_UpsertOverwrites(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	if err := repo.Upsert(ctx, "u1", []TileID{TileRunning, TileSteps}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	want := []TileID{TileNutrition, TileStreak}
	if err := repo.Upsert(ctx, "u1", want); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertTileIDs(t, got.TileIDs, want)
}

// TestLayout_FiltersUnknownIDs seeds a raw row containing a retired/unknown id
// and confirms Get drops it while preserving the order of the valid ids.
func TestLayout_FiltersUnknownIDs(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	_, err := db.Exec(
		`INSERT INTO user_dashboard_layouts (user_id, tile_ids, updated_at) VALUES (?, ?, ?)`,
		"u1", `["running","bogus_tile","steps"]`, "2026-01-01",
	)
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertTileIDs(t, got.TileIDs, []TileID{TileRunning, TileSteps})
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
// a non-nil, zero-length slice (not nil), so callers can distinguish "explicitly
// empty" from "no row" (which returns ErrLayoutNotFound).
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
	assertTileIDs(t, got.TileIDs, []TileID{})
}

// TestLayout_CascadeOnUserDelete confirms the ON DELETE CASCADE fires: deleting
// the parent user removes the layout row. dbtest opens with _foreign_keys=on
// (see db.Open), so the FK is enforced without an extra PRAGMA here.
func TestLayout_CascadeOnUserDelete(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteLayoutRepository(db)

	if err := repo.Upsert(ctx, "u1", []TileID{TileRunning}); err != nil {
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
