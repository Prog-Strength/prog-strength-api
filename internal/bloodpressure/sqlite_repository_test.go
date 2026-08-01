package bloodpressure

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

func intPtr(v int) *int { return &v }

func TestEntry_ValidateRejectsCheckBoundaries(t *testing.T) {
	base := func() *Entry {
		return &Entry{Systolic: 120, Diastolic: 80, MeasuredAt: time.Now()}
	}
	cases := []struct {
		name    string
		mutate  func(*Entry)
		wantErr error
	}{
		{"systolic too low", func(e *Entry) { e.Systolic = 49 }, ErrSystolicRange},
		{"systolic too high", func(e *Entry) { e.Systolic = 301; e.Diastolic = 80 }, ErrSystolicRange},
		{"diastolic too low", func(e *Entry) { e.Diastolic = 29 }, ErrDiastolicRange},
		{"diastolic too high", func(e *Entry) { e.Systolic = 250; e.Diastolic = 201 }, ErrDiastolicRange},
		{"inverted pair", func(e *Entry) { e.Systolic = 80; e.Diastolic = 120 }, ErrSystolicNotAboveDiastolic},
		{"equal pair", func(e *Entry) { e.Systolic = 90; e.Diastolic = 90 }, ErrSystolicNotAboveDiastolic},
		{"pulse too low", func(e *Entry) { e.Pulse = intPtr(19) }, ErrPulseRange},
		{"pulse too high", func(e *Entry) { e.Pulse = intPtr(251) }, ErrPulseRange},
		{"missing measured_at", func(e *Entry) { e.MeasuredAt = time.Time{} }, ErrMeasuredAtRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := base()
			tc.mutate(e)
			if err := e.Validate(); !errors.Is(err, tc.wantErr) {
				t.Errorf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestCreate_RejectsInvalidWithoutWriting(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	// Inverted pair: Create should surface the validation error and touch
	// nothing — the subsequent List proves no row was written.
	e := &Entry{UserID: "u1", Systolic: 80, Diastolic: 120, MeasuredAt: time.Now().UTC()}
	if err := repo.Create(ctx, e); !errors.Is(err, ErrSystolicNotAboveDiastolic) {
		t.Fatalf("want ErrSystolicNotAboveDiastolic, got %v", err)
	}
	if e.ID != "" {
		t.Errorf("failed create should not populate ID, got %q", e.ID)
	}
	got, err := repo.List(ctx, "u1", nil, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("failed create wrote a row: %+v", got)
	}
}

func TestCreateGet_RoundtripWithPulse(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	e := &Entry{
		UserID:     "u1",
		Systolic:   128,
		Diastolic:  82,
		Pulse:      intPtr(64),
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
	if got.Systolic != 128 || got.Diastolic != 82 {
		t.Errorf("wrong pressures: %+v", got)
	}
	if got.Pulse == nil || *got.Pulse != 64 {
		t.Errorf("pulse did not round-trip: %+v", got.Pulse)
	}
	if got.Category() != CategoryStage1 {
		t.Errorf("category = %q, want stage_1", got.Category())
	}
}

func TestCreateGet_RoundtripWithoutPulse(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	e := &Entry{
		UserID:     "u1",
		Systolic:   118,
		Diastolic:  76,
		MeasuredAt: time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC),
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.Get(ctx, "u1", e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Pulse != nil {
		t.Errorf("nil pulse should round-trip as nil, got %v", *got.Pulse)
	}
}

func TestList_FiltersByRangeAndSortsDescending(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	mustLog := func(t *testing.T, measuredAt time.Time, systolic int) {
		t.Helper()
		if err := repo.Create(ctx, &Entry{
			UserID:     "u1",
			Systolic:   systolic,
			Diastolic:  80,
			MeasuredAt: measuredAt,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	mustLog(t, time.Date(2026, 5, 27, 7, 0, 0, 0, time.UTC), 120)
	mustLog(t, time.Date(2026, 5, 28, 7, 0, 0, 0, time.UTC), 121)
	mustLog(t, time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC), 122)

	// Bounded range: just the middle entry.
	since := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	got, err := repo.List(ctx, "u1", &since, &until)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Systolic != 121 {
		t.Errorf("range filter wrong: %+v", got)
	}

	// Open-ended: all three, newest first.
	got, err = repo.List(ctx, "u1", nil, nil)
	if err != nil {
		t.Fatalf("list unbounded: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	if got[0].Systolic != 122 || got[2].Systolic != 120 {
		t.Errorf("expected DESC by measured_at, got %v", got)
	}
}

func TestDelete_SoftDeletesAndHidesFromGet(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	e := &Entry{UserID: "u1", Systolic: 120, Diastolic: 80, MeasuredAt: time.Now().UTC()}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.Delete(ctx, "u1", e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(ctx, "u1", e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: want ErrNotFound, got %v", err)
	}
	// Second delete finds nothing left to soft-delete.
	if err := repo.Delete(ctx, "u1", e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete: want ErrNotFound, got %v", err)
	}
}

func TestUpdateEntry_Partial(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	e := &Entry{
		UserID:     "u1",
		Systolic:   140,
		Diastolic:  92,
		Pulse:      intPtr(80),
		MeasuredAt: time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC),
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Correct the reading and drop the pulse to nil.
	e.Systolic = 122
	e.Diastolic = 78
	e.Pulse = nil
	if err := repo.UpdateEntry(ctx, e); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.Get(ctx, "u1", e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Systolic != 122 || got.Diastolic != 78 {
		t.Errorf("update did not persist pressures: %+v", got)
	}
	if got.Pulse != nil {
		t.Errorf("update should have cleared pulse, got %v", *got.Pulse)
	}
	if got.Category() != CategoryElevated {
		t.Errorf("category = %q, want elevated", got.Category())
	}
}

func TestOwnershipScoping(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	e := &Entry{UserID: "userA", Systolic: 120, Diastolic: 80, MeasuredAt: time.Now().UTC()}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// User B cannot Get, UpdateEntry, or Delete user A's row — each
	// collapses to ErrNotFound.
	if _, err := repo.Get(ctx, "userB", e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user Get: want ErrNotFound, got %v", err)
	}

	stolen := *e
	stolen.UserID = "userB"
	stolen.Systolic = 200
	stolen.Diastolic = 100
	if err := repo.UpdateEntry(ctx, &stolen); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user UpdateEntry: want ErrNotFound, got %v", err)
	}

	if err := repo.Delete(ctx, "userB", e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user Delete: want ErrNotFound, got %v", err)
	}

	// Owner's row is untouched by the failed cross-user attempts.
	got, err := repo.Get(ctx, "userA", e.ID)
	if err != nil {
		t.Fatalf("owner get: %v", err)
	}
	if got.Systolic != 120 {
		t.Errorf("owner row was mutated: %+v", got)
	}
}
