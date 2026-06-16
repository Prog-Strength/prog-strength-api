package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// seedUserHandle inserts a users row with explicit username/deleted_at so the
// backfill's NULL-username + soft-delete filters can be exercised precisely.
// username and deletedAt are passed as `any` so nil maps to SQL NULL.
func seedUserHandle(t *testing.T, db *sql.DB, id, displayName string, username, deletedAt any) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO users (id, email, display_name, username, weight_unit, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, 'lb', '2026-06-06', '2026-06-06', ?)
	`, id, id+"@example.com", displayName, username, deletedAt); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

// userHandle returns the stored username (and whether it is non-NULL) for id.
func userHandle(t *testing.T, db *sql.DB, id string) (string, bool) {
	t.Helper()
	var name sql.NullString
	if err := db.QueryRow(`SELECT username FROM users WHERE id = ?`, id).Scan(&name); err != nil {
		t.Fatalf("select username %s: %v", id, err)
	}
	return name.String, name.Valid
}

// runBackfillUsernames drives backfillUsernames inside a manual transaction,
// the same way migration 029 runs it, and commits.
func runBackfillUsernames(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := backfillUsernames(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestBackfill029_AssignsHandlesAndIsIdempotent seeds a mix of users and proves
// the backfill assigns valid, unique handles to live NULL-username users,
// leaves an existing handle untouched, never touches a soft-deleted user, and
// re-runs as a no-op.
func TestBackfill029_AssignsHandlesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)

	// Two distinct live users whose display names slugify to the same "sam_lifter"
	// base — they must receive DISTINCT valid handles via in-tx suffix retry.
	seedUserHandle(t, db, "u1", "Sam Lifter", nil, nil)
	seedUserHandle(t, db, "u2", "Sam Lifter", nil, nil)
	// A live user who already holds a handle: must be left exactly as-is.
	seedUserHandle(t, db, "u3", "Kept User", "keep_me", nil)
	// A soft-deleted NULL-username user: must NOT be assigned, stays NULL.
	seedUserHandle(t, db, "u4", "Ghost User", nil, "2026-06-06T00:00:00Z")

	runBackfillUsernames(t, db)

	h1, ok1 := userHandle(t, db, "u1")
	h2, ok2 := userHandle(t, db, "u2")
	if !ok1 || !ok2 {
		t.Fatalf("want both live null-username users assigned, got u1=%v u2=%v", ok1, ok2)
	}
	for id, h := range map[string]string{"u1": h1, "u2": h2} {
		if _, err := user.ValidateUsername(h); err != nil {
			t.Fatalf("%s got invalid handle %q: %v", id, h, err)
		}
	}
	if h1 == h2 {
		t.Fatalf("want distinct handles for the two Sam Lifters, both got %q", h1)
	}

	// keep_me untouched.
	if h3, _ := userHandle(t, db, "u3"); h3 != "keep_me" {
		t.Fatalf("want keep_me unchanged, got %q", h3)
	}

	// Soft-deleted user stays NULL.
	if _, ok4 := userHandle(t, db, "u4"); ok4 {
		t.Fatal("want soft-deleted user to stay null-username, got a handle")
	}

	// Global invariant: every non-deleted user has a unique, valid handle.
	assertEveryLiveUserHasUniqueValidHandle(t, db)

	// Re-run is a clean no-op: handles are unchanged.
	runBackfillUsernames(t, db)
	if again1, _ := userHandle(t, db, "u1"); again1 != h1 {
		t.Fatalf("idempotency: u1 changed from %q to %q on re-run", h1, again1)
	}
	if again2, _ := userHandle(t, db, "u2"); again2 != h2 {
		t.Fatalf("idempotency: u2 changed from %q to %q on re-run", h2, again2)
	}
	if h3, _ := userHandle(t, db, "u3"); h3 != "keep_me" {
		t.Fatalf("idempotency: keep_me changed to %q", h3)
	}
	if _, ok4 := userHandle(t, db, "u4"); ok4 {
		t.Fatal("idempotency: soft-deleted user gained a handle on re-run")
	}
}

// TestBackfill029_FreshDBNoOp verifies the backfill over a DB with no
// null-username users is an error-free no-op.
func TestBackfill029_FreshDBNoOp(t *testing.T) {
	t.Parallel()
	db := newMigratedDB(t)
	runBackfillUsernames(t, db)
}

// assertEveryLiveUserHasUniqueValidHandle checks the post-backfill invariant:
// every non-deleted user has a non-NULL handle that passes ValidateUsername,
// and no two live users share a handle.
func assertEveryLiveUserHasUniqueValidHandle(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT id, username FROM users WHERE deleted_at IS NULL`)
	if err != nil {
		t.Fatalf("query live users: %v", err)
	}
	defer rows.Close()

	seen := map[string]string{}
	for rows.Next() {
		var id string
		var name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !name.Valid {
			t.Fatalf("live user %s has NULL username after backfill", id)
		}
		if _, err := user.ValidateUsername(name.String); err != nil {
			t.Fatalf("live user %s has invalid handle %q: %v", id, name.String, err)
		}
		if prev, dup := seen[name.String]; dup {
			t.Fatalf("duplicate handle %q on users %s and %s", name.String, prev, id)
		}
		seen[name.String] = id
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
}
