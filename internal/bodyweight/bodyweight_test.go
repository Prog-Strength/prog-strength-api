package bodyweight

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

func TestEntry_ValidateRejectsNonPositiveWeight(t *testing.T) {
	e := Entry{Weight: 0, Unit: user.WeightUnitPounds, MeasuredAt: time.Now()}
	if err := e.Validate(); !errors.Is(err, ErrWeightNonPositive) {
		t.Errorf("want ErrWeightNonPositive, got %v", err)
	}
}

func TestEntry_ValidateRejectsInvalidUnit(t *testing.T) {
	e := Entry{Weight: 180, Unit: "stone", MeasuredAt: time.Now()}
	if err := e.Validate(); !errors.Is(err, ErrInvalidUnit) {
		t.Errorf("want ErrInvalidUnit, got %v", err)
	}
}

func TestEntry_ValidateRequiresMeasuredAt(t *testing.T) {
	e := Entry{Weight: 180, Unit: user.WeightUnitPounds}
	if err := e.Validate(); !errors.Is(err, ErrMeasuredAtRequired) {
		t.Errorf("want ErrMeasuredAtRequired, got %v", err)
	}
}

func TestCreateGetListDelete_Roundtrip(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	e := &Entry{
		UserID:     "u1",
		Weight:     185.5,
		Unit:       user.WeightUnitPounds,
		MeasuredAt: time.Date(2026, 5, 29, 7, 30, 0, 0, time.UTC),
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}
	if e.ID == "" {
		t.Fatal("create did not populate ID")
	}

	got, err := repo.Get(ctx, "u1", e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Weight != 185.5 || got.Unit != user.WeightUnitPounds {
		t.Errorf("get returned wrong shape: %+v", got)
	}

	// Cross-user lookup returns ErrNotFound — same convention as the
	// other domain repos.
	if _, err := repo.Get(ctx, "u2", e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user: want ErrNotFound, got %v", err)
	}

	if err := repo.Delete(ctx, "u1", e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, "u1", e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: want ErrNotFound, got %v", err)
	}
}

func TestList_FiltersByRangeAndSortsDescending(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	mustLog := func(t *testing.T, measuredAt time.Time, weight float64) {
		t.Helper()
		if err := repo.Create(ctx, &Entry{
			UserID:     "u1",
			Weight:     weight,
			Unit:       user.WeightUnitPounds,
			MeasuredAt: measuredAt,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// 3 entries across consecutive days. List should come back in
	// MeasuredAt-DESC order.
	mustLog(t, time.Date(2026, 5, 27, 7, 0, 0, 0, time.UTC), 184)
	mustLog(t, time.Date(2026, 5, 28, 7, 0, 0, 0, time.UTC), 185)
	mustLog(t, time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC), 186)

	// Bounded range: just the middle entry.
	since := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	got, err := repo.List(ctx, "u1", &since, &until)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Weight != 185 {
		t.Errorf("range filter wrong: %+v", got)
	}

	// Open-ended: all three.
	got, err = repo.List(ctx, "u1", nil, nil)
	if err != nil {
		t.Fatalf("list unbounded: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	if got[0].Weight != 186 || got[2].Weight != 184 {
		t.Errorf("expected DESC by measured_at, got %v", got)
	}
}

func TestList_ExcludesSoftDeleted(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	entry := &Entry{
		UserID:     "u1",
		Weight:     180,
		Unit:       user.WeightUnitPounds,
		MeasuredAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, entry); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.Delete(ctx, "u1", entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := repo.List(ctx, "u1", nil, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("soft-deleted should not appear in list, got %+v", got)
	}
}
