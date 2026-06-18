package beta

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// newSQLiteBetaRepo spins up a migrated, file-backed SQLite database and
// returns a beta repository over it. Mirrors internal/user's test helper.
func newSQLiteBetaRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "beta.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewSQLiteRepository(sqlDB)
}

func TestRepository_AddThenIsAllowed(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	if err := r.Add(ctx, "lifter@example.com", "admin@example.com", "early tester"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	allowed, err := r.IsAllowed(ctx, "lifter@example.com")
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("IsAllowed = false, want true for added email")
	}

	// Absent email is denied once the table is non-empty.
	absent, err := r.IsAllowed(ctx, "nobody@example.com")
	if err != nil {
		t.Fatalf("IsAllowed absent: %v", err)
	}
	if absent {
		t.Fatal("IsAllowed = true for absent email, want false")
	}
}

func TestRepository_CaseAndWhitespaceInsensitive(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	if err := r.Add(ctx, "  Foo@Bar.com ", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	allowed, err := r.IsAllowed(ctx, "foo@bar.com")
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("normalized lookup failed: 'Foo@Bar.com ' should match 'foo@bar.com'")
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Email != "foo@bar.com" {
		t.Fatalf("stored email not normalized: %+v", list)
	}
}

func TestRepository_AddIdempotent(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	if err := r.Add(ctx, "dup@example.com", "first@example.com", "original"); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	// Second add must not error and must not duplicate or overwrite.
	if err := r.Add(ctx, "dup@example.com", "second@example.com", "changed"); err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1 (no dup)", len(list))
	}
	// Original row preserved (INSERT OR IGNORE keeps the first).
	if list[0].AddedBy == nil || *list[0].AddedBy != "first@example.com" {
		t.Fatalf("original added_by overwritten: %+v", list[0].AddedBy)
	}
}

func TestRepository_RemoveTogglesPresence(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	if err := r.Add(ctx, "gone@example.com", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	removed, err := r.Remove(ctx, "Gone@Example.com")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed {
		t.Fatal("first Remove = false, want true")
	}
	removedAgain, err := r.Remove(ctx, "gone@example.com")
	if err != nil {
		t.Fatalf("Remove again: %v", err)
	}
	if removedAgain {
		t.Fatal("second Remove = true, want false")
	}
}

func TestRepository_ListOrderedByAddedAtAsc(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	// Stagger added_at via an injected clock so ordering is deterministic.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	times := []time.Time{base.Add(2 * time.Hour), base, base.Add(time.Hour)}
	i := 0
	r.now = func() time.Time {
		ts := times[i]
		i++
		return ts
	}

	// Add in a non-sorted order; expect List sorted by added_at asc.
	if err := r.Add(ctx, "c@example.com", "", ""); err != nil { // t=+2h
		t.Fatalf("Add c: %v", err)
	}
	if err := r.Add(ctx, "a@example.com", "", ""); err != nil { // t=base
		t.Fatalf("Add a: %v", err)
	}
	if err := r.Add(ctx, "b@example.com", "", ""); err != nil { // t=+1h
		t.Fatalf("Add b: %v", err)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"a@example.com", "b@example.com", "c@example.com"}
	if len(list) != len(want) {
		t.Fatalf("List len = %d, want %d", len(list), len(want))
	}
	for idx, e := range list {
		if e.Email != want[idx] {
			t.Fatalf("List[%d].Email = %s, want %s (order: %+v)", idx, e.Email, want[idx], list)
		}
	}
}

func TestRepository_EmptyTableAllowsEveryone(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	for _, email := range []string{"anyone@example.com", "another@example.com", ""} {
		allowed, err := r.IsAllowed(ctx, email)
		if err != nil {
			t.Fatalf("IsAllowed(%q): %v", email, err)
		}
		if !allowed {
			t.Fatalf("IsAllowed(%q) = false on empty table, want true (gate disabled)", email)
		}
	}
}

func TestRepository_NullableFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	// Empty addedBy/note must round-trip as nil pointers (SQL NULL).
	if err := r.Add(ctx, "nulls@example.com", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list[0].AddedBy != nil {
		t.Fatalf("added_by = %v, want nil", *list[0].AddedBy)
	}
	if list[0].Note != nil {
		t.Fatalf("note = %v, want nil", *list[0].Note)
	}
}
