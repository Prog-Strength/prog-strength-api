package workout

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
)

func TestReplaceAndListUserHeadlineExercises_Roundtrip(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	want := []string{"barbell-bench-press", "barbell-deadlift", "barbell-overhead-press"}
	if err := repo.ReplaceUserHeadlineExercises(ctx, "u1", want, now); err != nil {
		t.Fatalf("ReplaceUserHeadlineExercises: %v", err)
	}

	got, err := repo.ListUserHeadlineExercises(ctx, "u1")
	if err != nil {
		t.Fatalf("ListUserHeadlineExercises: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("list length: got %d, want %d", len(got), len(want))
	}
	for i, row := range got {
		if row.ExerciseID != want[i] {
			t.Errorf("row %d ExerciseID: got %q, want %q", i, row.ExerciseID, want[i])
		}
		if row.Position != i {
			t.Errorf("row %d Position: got %d, want %d", i, row.Position, i)
		}
		if row.UserID != "u1" {
			t.Errorf("row %d UserID: got %q, want %q", i, row.UserID, "u1")
		}
		if !row.CreatedAt.Equal(now) {
			t.Errorf("row %d CreatedAt: got %v, want %v", i, row.CreatedAt, now)
		}
	}
}

func TestReplaceUserHeadlineExercises_OverwritesPrevious(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	if err := repo.ReplaceUserHeadlineExercises(
		ctx, "u1", []string{"barbell-bench-press", "barbell-deadlift"}, now,
	); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	// A completely different selection overwrites — no merging.
	want := []string{"barbell-overhead-press", "neutral-grip-pull-up", "barbell-high-bar-back-squat"}
	if err := repo.ReplaceUserHeadlineExercises(ctx, "u1", want, now); err != nil {
		t.Fatalf("second replace: %v", err)
	}

	got, err := repo.ListUserHeadlineExercises(ctx, "u1")
	if err != nil {
		t.Fatalf("ListUserHeadlineExercises: %v", err)
	}
	gotIDs := make([]string, len(got))
	for i, r := range got {
		gotIDs[i] = r.ExerciseID
	}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("after overwrite: got %v, want %v", gotIDs, want)
	}
}

func TestReplaceUserHeadlineExercises_EmptyClearsRows(t *testing.T) {
	// The in-memory implementation treats an empty slice as "delete
	// the user's rows," matching the SQLite implementation. The
	// handler layer rejects empty PUTs upstream, but the repo
	// contract is broader so backfill / admin paths can use it.
	repo := NewMemoryRepository()
	ctx := context.Background()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	if err := repo.ReplaceUserHeadlineExercises(
		ctx, "u1", []string{"barbell-bench-press"}, now,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.ReplaceUserHeadlineExercises(ctx, "u1", nil, now); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := repo.ListUserHeadlineExercises(ctx, "u1")
	if err != nil {
		t.Fatalf("ListUserHeadlineExercises: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list after clear, got %d rows", len(got))
	}
}

func TestEffectiveHeadlineExerciseSlugs_DefaultsWhenNoCustom(t *testing.T) {
	repo := NewMemoryRepository()
	exRepo := exercise.NewMemoryRepository(exercise.Catalog)
	h := NewHandler(repo, exRepo)

	got, err := h.effectiveHeadlineExerciseSlugs(context.Background(), "u-never-customized")
	if err != nil {
		t.Fatalf("effectiveHeadlineExerciseSlugs: %v", err)
	}
	if !reflect.DeepEqual(got, HeadlineExercises) {
		t.Errorf("expected defaults, got %v", got)
	}
	// Defensive copy: mutating the returned slice must not affect
	// the package-level HeadlineExercises.
	if len(got) > 0 {
		got[0] = "mutated"
		if HeadlineExercises[0] == "mutated" {
			t.Error("returned slice aliases the package-level HeadlineExercises")
		}
	}
}

func TestEffectiveHeadlineExerciseSlugs_ReturnsCustom(t *testing.T) {
	repo := NewMemoryRepository()
	exRepo := exercise.NewMemoryRepository(exercise.Catalog)
	h := NewHandler(repo, exRepo)
	ctx := context.Background()
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	want := []string{"barbell-overhead-press", "neutral-grip-pull-up"}
	if err := repo.ReplaceUserHeadlineExercises(ctx, "u1", want, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := h.effectiveHeadlineExerciseSlugs(ctx, "u1")
	if err != nil {
		t.Fatalf("effectiveHeadlineExerciseSlugs: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
