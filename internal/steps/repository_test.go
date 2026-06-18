package steps

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// strptr is a helper for the *string range/keyset bounds.
func strptr(s string) *string { return &s }

// --- UpsertEntry: replace, don't duplicate -----------------------------

func TestUpsertEntry_ReplacesNotDuplicates(t *testing.T) {
	ctx := context.Background()
	assertUpsertReplaces(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertUpsertReplaces(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	// Log the same (user, date) twice with different counts.
	first, err := repo.UpsertEntry(ctx, &Entry{UserID: "u1", Date: "2026-06-14", Steps: 8000})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.ID == "" {
		t.Fatal("upsert did not populate ID")
	}
	if first.Steps != 8000 {
		t.Fatalf("steps = %d, want 8000", first.Steps)
	}

	// Force a strictly-later updated_at so the bump is observable.
	time.Sleep(2 * time.Millisecond)
	second, err := repo.UpsertEntry(ctx, &Entry{UserID: "u1", Date: "2026-06-14", Steps: 9500})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.Steps != 9500 {
		t.Errorf("steps not replaced: %d, want 9500", second.Steps)
	}
	// created_at preserved across the upsert.
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at should be preserved: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	// updated_at bumped.
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("updated_at should bump: first=%v second=%v", first.UpdatedAt, second.UpdatedAt)
	}

	// Exactly one row for the (user, date) — a range read covering the day
	// returns a single entry.
	got, _, err := repo.List(ctx, "u1", strptr("2026-06-14"), strptr("2026-06-14"), 0, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row after re-log, got %d: %+v", len(got), got)
	}
	if got[0].Steps != 9500 {
		t.Errorf("listed steps = %d, want 9500", got[0].Steps)
	}
}

func TestUpsertEntry_RejectsOutOfRange(t *testing.T) {
	ctx := context.Background()
	assertUpsertOutOfRange(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertUpsertOutOfRange(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "u1", Date: "2026-06-14", Steps: MaxSteps + 1}); !errors.Is(err, ErrStepsOutOfRange) {
		t.Errorf("over-max: want ErrStepsOutOfRange, got %v", err)
	}
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "u1", Date: "2026-06-14", Steps: -1}); !errors.Is(err, ErrStepsOutOfRange) {
		t.Errorf("negative: want ErrStepsOutOfRange, got %v", err)
	}
}

// --- List: range mode (inclusive bounds) -------------------------------

func TestList_RangeInclusiveBounds(t *testing.T) {
	ctx := context.Background()
	assertRangeInclusive(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertRangeInclusive(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	seedDays(t, repo, ctx, "u1", map[string]int{
		"2026-06-10": 100,
		"2026-06-11": 200,
		"2026-06-12": 300,
		"2026-06-13": 400,
	})

	// since and until both inclusive: 11..13 → three rows, newest first.
	got, nextBefore, err := repo.List(ctx, "u1", strptr("2026-06-11"), strptr("2026-06-13"), 0, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if nextBefore != "" {
		t.Errorf("range mode next_before should be empty, got %q", nextBefore)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(got), got)
	}
	if got[0].Date != "2026-06-13" || got[2].Date != "2026-06-11" {
		t.Errorf("expected DESC by date 13..11, got %v", datesOf(got))
	}
}

// --- List: keyset mode + next_before -----------------------------------

func TestList_KeysetPagination(t *testing.T) {
	ctx := context.Background()
	assertKeyset(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertKeyset(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	seedDays(t, repo, ctx, "u1", map[string]int{
		"2026-06-10": 100,
		"2026-06-11": 200,
		"2026-06-12": 300,
		"2026-06-13": 400,
		"2026-06-14": 500,
	})

	// First page: limit 2 → newest two, next_before = last row's date.
	page1, nb1, err := repo.List(ctx, "u1", nil, nil, 2, nil)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || page1[0].Date != "2026-06-14" || page1[1].Date != "2026-06-13" {
		t.Fatalf("page1 wrong: %v", datesOf(page1))
	}
	if nb1 != "2026-06-13" {
		t.Fatalf("page1 next_before = %q, want 2026-06-13", nb1)
	}

	// Second page using the cursor.
	page2, nb2, err := repo.List(ctx, "u1", nil, nil, 2, strptr(nb1))
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || page2[0].Date != "2026-06-12" || page2[1].Date != "2026-06-11" {
		t.Fatalf("page2 wrong: %v", datesOf(page2))
	}
	if nb2 != "2026-06-11" {
		t.Fatalf("page2 next_before = %q, want 2026-06-11", nb2)
	}

	// Final partial page: one row left, no full page → empty next_before.
	page3, nb3, err := repo.List(ctx, "u1", nil, nil, 2, strptr(nb2))
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 || page3[0].Date != "2026-06-10" {
		t.Fatalf("page3 wrong: %v", datesOf(page3))
	}
	if nb3 != "" {
		t.Errorf("page3 next_before should be empty (partial page), got %q", nb3)
	}
}

func TestList_KeysetWinsOverRange(t *testing.T) {
	ctx := context.Background()
	repo := NewSQLiteRepository(dbtest.New(t))
	seedDays(t, repo, ctx, "u1", map[string]int{
		"2026-06-10": 100,
		"2026-06-11": 200,
		"2026-06-12": 300,
	})
	// limit set alongside since/until: keyset wins, since/until ignored.
	got, _, err := repo.List(ctx, "u1", strptr("2026-06-12"), strptr("2026-06-12"), 10, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("keyset should ignore since/until, expected 3 got %d: %v", len(got), datesOf(got))
	}
}

// --- Delete ------------------------------------------------------------

func TestDelete_HardDeleteAndNotFound(t *testing.T) {
	ctx := context.Background()
	assertDelete(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertDelete(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "u1", Date: "2026-06-14", Steps: 8000}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.Delete(ctx, "u1", "2026-06-14"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Gone for good — no row remains.
	got, _, err := repo.List(ctx, "u1", strptr("2026-06-14"), strptr("2026-06-14"), 0, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("hard delete should remove the row, got %+v", got)
	}
	// Second delete → ErrNotFound.
	if err := repo.Delete(ctx, "u1", "2026-06-14"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete absent: want ErrNotFound, got %v", err)
	}
}

func TestDelete_CrossUserNotFound(t *testing.T) {
	ctx := context.Background()
	assertDeleteCrossUser(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertDeleteCrossUser(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "user-a", Date: "2026-06-14", Steps: 8000}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// user-b must not delete user-a's day.
	if err := repo.Delete(ctx, "user-b", "2026-06-14"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user delete: want ErrNotFound, got %v", err)
	}
	// And user-a's row is untouched.
	got, _, err := repo.List(ctx, "user-a", strptr("2026-06-14"), strptr("2026-06-14"), 0, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("cross-user delete leaked: user-a row count = %d, want 1", len(got))
	}
}

// --- List: authz isolation ---------------------------------------------

func TestList_ScopedToUser(t *testing.T) {
	ctx := context.Background()
	assertListScoped(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertListScoped(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "user-a", Date: "2026-06-14", Steps: 8000}); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if _, err := repo.UpsertEntry(ctx, &Entry{UserID: "user-b", Date: "2026-06-14", Steps: 1}); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	got, _, err := repo.List(ctx, "user-b", nil, nil, 10, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Steps != 1 {
		t.Errorf("user-b should only see their own row, got %+v", got)
	}
}

// --- Goal upsert -------------------------------------------------------

func TestGoal_InsertThenUpdate(t *testing.T) {
	ctx := context.Background()
	assertGoalRoundtrip(t, NewSQLiteRepository(dbtest.New(t)), ctx)
}

func assertGoalRoundtrip(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	// Empty state: zero goal, nil timestamps.
	g, err := repo.GetGoal(ctx, "user-a")
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if g.Goal != 0 || g.CreatedAt != nil || g.UpdatedAt != nil {
		t.Errorf("never-set goal should be zero with nil timestamps, got %+v", g)
	}

	t0 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	first, err := repo.UpsertGoal(ctx, Goal{UserID: "user-a", Goal: 10000}, t0)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.Goal != 10000 || first.CreatedAt == nil || !first.CreatedAt.Equal(t0) || !first.UpdatedAt.Equal(t0) {
		t.Fatalf("first goal wrong: %+v", first)
	}

	t1 := t0.Add(2 * time.Hour)
	second, err := repo.UpsertGoal(ctx, Goal{UserID: "user-a", Goal: 12000}, t1)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.Goal != 12000 {
		t.Errorf("goal not replaced: %d, want 12000", second.Goal)
	}
	if !second.CreatedAt.Equal(t0) {
		t.Errorf("created_at should be preserved: %v", second.CreatedAt)
	}
	if !second.UpdatedAt.Equal(t1) {
		t.Errorf("updated_at should bump to t1: %v", second.UpdatedAt)
	}
}

// --- test helpers ------------------------------------------------------

func seedDays(t *testing.T, repo Repository, ctx context.Context, userID string, days map[string]int) {
	t.Helper()
	for date, count := range days {
		if _, err := repo.UpsertEntry(ctx, &Entry{UserID: userID, Date: date, Steps: count}); err != nil {
			t.Fatalf("seed %s: %v", date, err)
		}
	}
}

func datesOf(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Date
	}
	return out
}
