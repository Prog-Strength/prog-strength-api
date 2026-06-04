package bodyweight

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// newSQLiteTestRepo spins up a migrated SQLite DB in a temp dir and
// returns a repository against it. Exercises the real SQL (and the
// 011_user_bodyweight_goal migration) end to end.
func newSQLiteTestRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewSQLiteRepository(d)
}

// --- GetBodyweightGoal / UpsertBodyweightGoal --------------------------

func TestGetBodyweightGoal_EmptyState(t *testing.T) {
	ctx := context.Background()
	t.Run("memory", func(t *testing.T) {
		assertEmptyGoal(t, NewMemoryRepository(), ctx)
	})
	t.Run("sqlite", func(t *testing.T) {
		assertEmptyGoal(t, newSQLiteTestRepo(t), ctx)
	})
}

func assertEmptyGoal(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	g, err := repo.GetBodyweightGoal(ctx, "user-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.UserID != "user-a" {
		t.Errorf("user id = %q, want user-a", g.UserID)
	}
	if g.Weight != 0 {
		t.Errorf("never-set weight = %v, want 0", g.Weight)
	}
	if g.CreatedAt != nil || g.UpdatedAt != nil {
		t.Errorf("never-set should have nil timestamps, got %v / %v", g.CreatedAt, g.UpdatedAt)
	}
}

func TestUpsertBodyweightGoal_InsertThenUpdate(t *testing.T) {
	ctx := context.Background()
	t.Run("memory", func(t *testing.T) {
		assertUpsertRoundtrip(t, NewMemoryRepository(), ctx)
	})
	t.Run("sqlite", func(t *testing.T) {
		assertUpsertRoundtrip(t, newSQLiteTestRepo(t), ctx)
	})
}

func assertUpsertRoundtrip(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	t0 := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	first, err := repo.UpsertBodyweightGoal(ctx, Goal{
		UserID: "user-a", Weight: 175, Unit: user.WeightUnitPounds,
	}, t0)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.CreatedAt == nil || !first.CreatedAt.Equal(t0) {
		t.Fatalf("created_at not set to t0: %v", first.CreatedAt)
	}
	if first.UpdatedAt == nil || !first.UpdatedAt.Equal(t0) {
		t.Fatalf("updated_at not set to t0: %v", first.UpdatedAt)
	}
	if first.Weight != 175 {
		t.Fatalf("weight = %v, want 175", first.Weight)
	}

	// Second upsert with a different weight replaces the value, preserves
	// created_at, and bumps updated_at to the later time.
	t1 := t0.Add(2 * time.Hour)
	second, err := repo.UpsertBodyweightGoal(ctx, Goal{
		UserID: "user-a", Weight: 168, Unit: user.WeightUnitKilograms,
	}, t1)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.Weight != 168 || second.Unit != user.WeightUnitKilograms {
		t.Errorf("update did not replace value: %+v", second)
	}
	if !second.CreatedAt.Equal(*first.CreatedAt) {
		t.Errorf("created_at should be preserved: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.Equal(t1) {
		t.Errorf("updated_at should bump to t1: %v", second.UpdatedAt)
	}
	if second.UpdatedAt.Before(*second.CreatedAt) {
		t.Errorf("updated_at %v should be >= created_at %v", second.UpdatedAt, second.CreatedAt)
	}

	// Read-back matches the second write.
	got, err := repo.GetBodyweightGoal(ctx, "user-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Weight != 168 || !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t1) {
		t.Errorf("read-back mismatch: %+v", got)
	}
}

// --- UpdateEntry -------------------------------------------------------

func TestUpdateEntry_HappyPath(t *testing.T) {
	ctx := context.Background()
	t.Run("memory", func(t *testing.T) {
		assertUpdateEntryHappy(t, NewMemoryRepository(), ctx)
	})
	t.Run("sqlite", func(t *testing.T) {
		assertUpdateEntryHappy(t, newSQLiteTestRepo(t), ctx)
	})
}

func assertUpdateEntryHappy(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	e := &Entry{
		UserID:     "u1",
		Weight:     185,
		Unit:       user.WeightUnitPounds,
		MeasuredAt: time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC),
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	origCreatedAt := e.CreatedAt

	newMeasured := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	upd := &Entry{
		ID:         e.ID,
		UserID:     "u1",
		Weight:     82,
		Unit:       user.WeightUnitKilograms,
		MeasuredAt: newMeasured,
	}
	if err := repo.UpdateEntry(ctx, upd); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.Get(ctx, "u1", e.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Weight != 82 || got.Unit != user.WeightUnitKilograms || !got.MeasuredAt.Equal(newMeasured) {
		t.Errorf("update did not persist: %+v", got)
	}
	if !got.CreatedAt.Equal(origCreatedAt) {
		t.Errorf("created_at should be preserved: orig=%v got=%v", origCreatedAt, got.CreatedAt)
	}
}

func TestUpdateEntry_SoftDeletedReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	t.Run("memory", func(t *testing.T) {
		assertUpdateSoftDeleted(t, NewMemoryRepository(), ctx)
	})
	t.Run("sqlite", func(t *testing.T) {
		assertUpdateSoftDeleted(t, newSQLiteTestRepo(t), ctx)
	})
}

func assertUpdateSoftDeleted(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	e := &Entry{
		UserID:     "u1",
		Weight:     180,
		Unit:       user.WeightUnitPounds,
		MeasuredAt: time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC),
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.Delete(ctx, "u1", e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	upd := &Entry{ID: e.ID, UserID: "u1", Weight: 181, Unit: user.WeightUnitPounds, MeasuredAt: e.MeasuredAt}
	if err := repo.UpdateEntry(ctx, upd); !errors.Is(err, ErrNotFound) {
		t.Errorf("update soft-deleted: want ErrNotFound, got %v", err)
	}
}

func TestUpdateEntry_CrossUserReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	t.Run("memory", func(t *testing.T) {
		assertUpdateCrossUser(t, NewMemoryRepository(), ctx)
	})
	t.Run("sqlite", func(t *testing.T) {
		assertUpdateCrossUser(t, newSQLiteTestRepo(t), ctx)
	})
}

func assertUpdateCrossUser(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	e := &Entry{
		UserID:     "user-a",
		Weight:     180,
		Unit:       user.WeightUnitPounds,
		MeasuredAt: time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC),
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Entry belongs to user-a; user-b must not be able to update it.
	upd := &Entry{ID: e.ID, UserID: "user-b", Weight: 999, Unit: user.WeightUnitPounds, MeasuredAt: e.MeasuredAt}
	if err := repo.UpdateEntry(ctx, upd); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user update: want ErrNotFound, got %v", err)
	}
	// And user-a's entry is untouched.
	got, err := repo.Get(ctx, "user-a", e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Weight != 180 {
		t.Errorf("cross-user update leaked: weight = %v, want 180", got.Weight)
	}
}
