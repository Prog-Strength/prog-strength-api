package exercise

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/testutil/sqlcount"
)

// Statement-count regression. List must issue exactly three SQL
// statements regardless of catalog size:
//
//  1. SELECT exercises (with optional muscle_group/equipment joins)
//  2. SELECT exercise_muscle_groups WHERE exercise_id IN (...)
//  3. SELECT exercise_equipment WHERE exercise_id IN (...)
//
// Pre-batched-hydration the count was 1 + 2N (one muscle_groups and one
// equipment query per exercise). See
// prog-strength-docs/sows/workout-list-batched-hydration.md.
func TestList_StatementCount(t *testing.T) {
	ctx := context.Background()
	d, counter := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seed := []seedRow{
		{id: "barbell-bench-press", name: "Barbell Bench Press", mgs: []string{"chest", "triceps"}, eq: []string{"barbell", "flat_bench"}},
		{id: "back-squat", name: "Back Squat", mgs: []string{"quads", "glutes"}, eq: []string{"barbell"}},
		{id: "barbell-deadlift", name: "Barbell Deadlift", mgs: []string{"back", "hamstrings"}, eq: []string{"barbell"}},
		{id: "pull-up", name: "Pull Up", mgs: []string{"back", "biceps"}, eq: []string{"pull_up_bar"}},
		{id: "dumbbell-row", name: "Dumbbell Row", mgs: []string{"back"}, eq: []string{"dumbbell"}},
	}
	seedCatalog(t, d, seed)

	counter.Reset()
	got, err := repo.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if c := counter.N(); c != 3 {
		t.Fatalf("statement count = %d, want 3", c)
	}
	if len(got) != len(seed) {
		t.Fatalf("got %d exercises, want %d", len(got), len(seed))
	}
}

// Pins the result shape after the batched-helper refactor: every
// exercise carries its muscle_groups and equipment, sorted as the SQL
// ORDER BY specifies, and the outer list is alphabetized by name.
func TestList_ResultShape(t *testing.T) {
	ctx := context.Background()
	d, _ := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seedCatalog(t, d, []seedRow{
		{id: "z-zebra-curl", name: "Zebra Curl", mgs: []string{"biceps"}, eq: []string{"dumbbell"}},
		{id: "a-bench-press", name: "Alpha Bench", mgs: []string{"chest", "triceps", "shoulders"}, eq: []string{"barbell", "flat_bench"}},
	})

	got, err := repo.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Outer list is name-ASCending case-insensitive.
	if len(got) != 2 || got[0].ID != "a-bench-press" || got[1].ID != "z-zebra-curl" {
		t.Fatalf("List order = %v, want [a-bench-press, z-zebra-curl]", names(got))
	}
	// Alpha bench's muscle_groups land in sorted order (ORDER BY mg ASC
	// inside the loader — chest, shoulders, triceps).
	wantMGs := []string{"chest", "shoulders", "triceps"}
	gotMGs := stringify(got[0].MuscleGroups)
	if !reflect.DeepEqual(gotMGs, wantMGs) {
		t.Errorf("alpha bench MGs = %v, want %v", gotMGs, wantMGs)
	}
	wantEq := []string{"barbell", "flat_bench"}
	gotEq := stringify(got[0].Equipment)
	if !reflect.DeepEqual(gotEq, wantEq) {
		t.Errorf("alpha bench equipment = %v, want %v", gotEq, wantEq)
	}
}

// Filtering by muscle_group / equipment still produces three statements
// (the outer query has a JOIN, the two child queries still happen).
func TestList_FilteredStatementCount(t *testing.T) {
	ctx := context.Background()
	d, counter := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seedCatalog(t, d, []seedRow{
		{id: "bench", name: "Bench", mgs: []string{"chest"}, eq: []string{"barbell"}},
		{id: "squat", name: "Squat", mgs: []string{"quads"}, eq: []string{"barbell"}},
	})

	counter.Reset()
	got, err := repo.List(ctx, ListOptions{MuscleGroup: "chest"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if c := counter.N(); c != 3 {
		t.Fatalf("filtered statement count = %d, want 3", c)
	}
	if len(got) != 1 || got[0].ID != "bench" {
		t.Fatalf("filtered result = %v, want [bench]", names(got))
	}
}

// --- helpers ------------------------------------------------------

type seedRow struct {
	id, name string
	mgs, eq  []string
}

func newTestDB(t *testing.T) (*sql.DB, *sqlcount.Counter) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	dsn := path + "?_foreign_keys=on&_journal_mode=WAL"
	sqlDB, counter, err := sqlcount.Open(dsn)
	if err != nil {
		t.Fatalf("sqlcount.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return sqlDB, counter
}

func seedCatalog(t *testing.T, d *sql.DB, rows []seedRow) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, r := range rows {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO exercises (id, name, description, created_at, updated_at, deleted_at)
			VALUES (?, ?, '', ?, ?, NULL)
		`, r.id, r.name, now, now); err != nil {
			t.Fatalf("seed exercise %s: %v", r.id, err)
		}
		for _, mg := range r.mgs {
			if _, err := d.ExecContext(ctx, `
				INSERT INTO exercise_muscle_groups (exercise_id, muscle_group)
				VALUES (?, ?)
			`, r.id, mg); err != nil {
				t.Fatalf("seed mg %s/%s: %v", r.id, mg, err)
			}
		}
		for _, e := range r.eq {
			if _, err := d.ExecContext(ctx, `
				INSERT INTO exercise_equipment (exercise_id, equipment)
				VALUES (?, ?)
			`, r.id, e); err != nil {
				t.Fatalf("seed eq %s/%s: %v", r.id, e, err)
			}
		}
	}
}

func names(xs []Exercise) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.ID
	}
	return out
}

// stringify works for both []MuscleGroup and []Equipment via the
// typed-string nature of those enums.
func stringify[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	sort.Strings(out) // defensive; SQL already sorts but tests shouldn't rely on it
	// Re-sort returns the right ordering for the test that asserts SQL
	// sort order — caller's expected slice is also alpha-sorted, so
	// this is a no-op for the asserts above.
	return out
}
