package db

import (
	"database/sql"
	"testing"
)

// seedLegacyTimeline writes the pre-046 feed state: posts under the old
// per-sport source types plus one comment and one reaction, so the rebuild has
// children to lose if the drop order is wrong.
func seedLegacyTimeline(t *testing.T, db *sql.DB) {
	t.Helper()
	const ts = "2026-05-01T09:00:00Z"
	for _, p := range []struct{ id, sourceType, sourceID string }{
		{"p1", "workout", "w1"},
		{"p2", "run", "a1"},
		{"p3", "pr", "pr1"},
		{"p4", "best_effort", "a1:5k"},
	} {
		if _, err := db.Exec(`
			INSERT INTO timeline_post (id, user_id, source_type, source_id, occurred_at, visibility, created_at, updated_at)
			VALUES (?, 'u1', ?, ?, ?, 'friends', ?, ?)
		`, p.id, p.sourceType, p.sourceID, ts, ts, ts); err != nil {
			t.Fatalf("seed post %s: %v", p.id, err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO timeline_comment (id, post_id, user_id, body, created_at, updated_at)
		VALUES ('c1', 'p1', 'u2', 'strong work', ?, ?)
	`, ts, ts); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO timeline_reaction (id, post_id, user_id, type, created_at)
		VALUES ('r1', 'p2', 'u2', 'fire', ?)
	`, ts); err != nil {
		t.Fatalf("seed reaction: %v", err)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// TestMigrate046_CollapsesLegacySourceTypes seeds the pre-046 feed just before
// the rebuild and verifies the collapse: 'workout' and 'run' both become
// 'activity', 'pr'/'best_effort' are untouched, and — the part that is easy to
// get wrong — every comment and reaction survives. DROP TABLE fires ON DELETE
// CASCADE, so rebuilding the children after dropping the parent would silently
// empty both tables (the lesson migrations 033 and 042 record).
func TestMigrate046_CollapsesLegacySourceTypes(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	applyMigrationsThrough(t, db, 0, 46, seedLegacyTimeline)

	if n := countRows(t, db, `SELECT COUNT(*) FROM timeline_post`); n != 4 {
		t.Fatalf("posts after rebuild = %d, want 4 (none lost)", n)
	}
	if n := countRows(t, db,
		`SELECT COUNT(*) FROM timeline_post WHERE source_type = 'activity' AND source_id IN ('w1','a1')`); n != 2 {
		t.Errorf("collapsed activity posts = %d, want 2 (w1 and a1)", n)
	}
	if n := countRows(t, db,
		`SELECT COUNT(*) FROM timeline_post WHERE source_type IN ('workout','run')`); n != 0 {
		t.Errorf("%d posts still carry a legacy per-sport source_type", n)
	}
	if n := countRows(t, db,
		`SELECT COUNT(*) FROM timeline_post WHERE source_type = 'pr' AND source_id = 'pr1'`); n != 1 {
		t.Error("pr post should be untouched by the collapse")
	}
	if n := countRows(t, db,
		`SELECT COUNT(*) FROM timeline_post WHERE source_type = 'best_effort' AND source_id = 'a1:5k'`); n != 1 {
		t.Error("best_effort post should be untouched by the collapse")
	}

	// Visibility and timestamps ride through the rebuild unchanged.
	if n := countRows(t, db,
		`SELECT COUNT(*) FROM timeline_post WHERE visibility = 'friends'`); n != 4 {
		t.Error("visibility did not survive the rebuild")
	}

	// The children the cascade would have eaten.
	if n := countRows(t, db, `SELECT COUNT(*) FROM timeline_comment WHERE id = 'c1' AND post_id = 'p1'`); n != 1 {
		t.Error("comment c1 did not survive the parent rebuild (cascade?)")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM timeline_reaction WHERE id = 'r1' AND post_id = 'p2'`); n != 1 {
		t.Error("reaction r1 did not survive the parent rebuild (cascade?)")
	}

	// The rebuilt children's FKs point at the renamed parent, so the cascade
	// still works for a real delete.
	if _, err := db.Exec(`DELETE FROM timeline_post WHERE id = 'p1'`); err != nil {
		t.Fatalf("delete post: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM timeline_comment WHERE post_id = 'p1'`); n != 0 {
		t.Error("deleting a post did not cascade to its comments — the child FK lost its target")
	}

	// Indexes are recreated with the tables.
	for _, idx := range []string{
		"idx_timeline_post_feed", "idx_timeline_comment_post", "idx_timeline_reaction_post",
	} {
		if !indexExists(t, db, idx) {
			t.Errorf("index %s missing after migration 046", idx)
		}
	}
}

// TestMigrate046_CheckAdmitsActivityOnly pins the widened CHECK: the coarse
// source domain is accepted and a per-sport value is not, which is what keeps
// the sport taxonomy in the Go registry where new types are free.
func TestMigrate046_CheckAdmitsActivityOnly(t *testing.T) {
	t.Parallel()
	db := newEmptyDB(t)
	applyMigrationsThrough(t, db, 0, 0, nil)

	const ts = "2026-05-01T09:00:00Z"
	insert := func(id, sourceType string) error {
		_, err := db.Exec(`
			INSERT INTO timeline_post (id, user_id, source_type, source_id, occurred_at, visibility, created_at, updated_at)
			VALUES (?, 'u1', ?, 'src', ?, 'private', ?, ?)
		`, id, sourceType, ts, ts, ts)
		return err
	}

	for _, st := range []string{"activity", "pr", "best_effort"} {
		if err := insert("ok-"+st, st); err != nil {
			t.Errorf("source_type %q should be accepted: %v", st, err)
		}
	}
	for _, st := range []string{"workout", "run", "hiking"} {
		if err := insert("bad-"+st, st); err == nil {
			t.Errorf("source_type %q should be rejected by the CHECK", st)
		}
	}
}
