package whooprecovery

import (
	"context"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

func fptr(f float64) *float64 { return &f }

// --- Upsert: replace, don't duplicate; preserve id + created_at --------

func TestUpsert_ReplacesNotDuplicates(t *testing.T) {
	ctx := context.Background()
	repo := NewSQLiteRepository(dbtest.New(t))

	t0 := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, Entry{
		UserID: "u1", Date: "2026-06-14",
		RecoveryScore: fptr(72), RestingHeartRate: fptr(55), HRVRmssdMilli: fptr(40),
		CycleID: 100, SleepID: "sleep-a",
	}, t0); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	got, err := repo.ListRange(ctx, "u1", "2026-06-14", "2026-06-14")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	first := got[0]
	if first.ID == "" {
		t.Fatal("upsert did not populate ID")
	}
	if *first.RecoveryScore != 72 || first.CycleID != 100 || first.SleepID != "sleep-a" {
		t.Fatalf("unexpected first row: %+v", first)
	}

	// Re-upsert same (user, date) with different values at a later time.
	t1 := t0.Add(2 * time.Hour)
	if err = repo.Upsert(ctx, Entry{
		UserID: "u1", Date: "2026-06-14",
		RecoveryScore: fptr(85), RestingHeartRate: fptr(52), HRVRmssdMilli: fptr(48),
		CycleID: 101, SleepID: "sleep-b",
	}, t1); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err = repo.ListRange(ctx, "u1", "2026-06-14", "2026-06-14")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 row after re-upsert, got %d: %+v", len(got), got)
	}
	second := got[0]

	if *second.RecoveryScore != 85 || *second.RestingHeartRate != 52 || *second.HRVRmssdMilli != 48 {
		t.Errorf("metrics not replaced: %+v", second)
	}
	if second.CycleID != 101 || second.SleepID != "sleep-b" {
		t.Errorf("cycle_id/sleep_id not replaced: %+v", second)
	}
	if second.ID != first.ID {
		t.Errorf("id should be preserved: first=%s second=%s", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at should be preserved: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("updated_at should advance: first=%v second=%v", first.UpdatedAt, second.UpdatedAt)
	}
	if !second.UpdatedAt.Equal(t1.UTC()) {
		t.Errorf("updated_at = %v, want %v", second.UpdatedAt, t1.UTC())
	}
}

// --- Nullable round-trip -----------------------------------------------

func TestUpsert_NullableRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewSQLiteRepository(dbtest.New(t))
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// All three metrics nil → stored NULL, read back nil.
	if err := repo.Upsert(ctx, Entry{
		UserID: "u1", Date: "2026-06-01",
		RecoveryScore: nil, RestingHeartRate: nil, HRVRmssdMilli: nil,
		CycleID: 1, SleepID: "s-nil",
	}, now); err != nil {
		t.Fatalf("upsert nil: %v", err)
	}
	got, err := repo.ListRange(ctx, "u1", "2026-06-01", "2026-06-01")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].RecoveryScore != nil || got[0].RestingHeartRate != nil || got[0].HRVRmssdMilli != nil {
		t.Errorf("nil metrics should read back nil, got %+v", got[0])
	}

	// Non-nil values read back with the stored value.
	if err = repo.Upsert(ctx, Entry{
		UserID: "u1", Date: "2026-06-02",
		RecoveryScore: fptr(60), RestingHeartRate: fptr(58), HRVRmssdMilli: fptr(33.5),
		CycleID: 2, SleepID: "s-val",
	}, now); err != nil {
		t.Fatalf("upsert val: %v", err)
	}
	got, err = repo.ListRange(ctx, "u1", "2026-06-02", "2026-06-02")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].RecoveryScore == nil || *got[0].RecoveryScore != 60 {
		t.Errorf("recovery_score round-trip failed: %+v", got)
	}
	if got[0].RestingHeartRate == nil || *got[0].RestingHeartRate != 58 {
		t.Errorf("resting_heart_rate round-trip failed: %+v", got[0])
	}
	if got[0].HRVRmssdMilli == nil || *got[0].HRVRmssdMilli != 33.5 {
		t.Errorf("hrv round-trip failed: %+v", got[0])
	}
}

// --- ListRange: windowing + ordering + isolation -----------------------

func TestListRange_WindowOrderingAndIsolation(t *testing.T) {
	ctx := context.Background()
	repo := NewSQLiteRepository(dbtest.New(t))
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	seed := func(user, date string) {
		if err := repo.Upsert(ctx, Entry{
			UserID: user, Date: date, RecoveryScore: fptr(70),
			CycleID: 1, SleepID: user + "-" + date,
		}, now); err != nil {
			t.Fatalf("seed %s/%s: %v", user, date, err)
		}
	}
	for _, d := range []string{"2026-06-10", "2026-06-11", "2026-06-12", "2026-06-13"} {
		seed("u1", d)
	}
	seed("u2", "2026-06-11")

	datesOf := func(es []Entry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.Date
		}
		return out
	}

	// Inclusive window 11..13, DESC.
	got, err := repo.ListRange(ctx, "u1", "2026-06-11", "2026-06-13")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"2026-06-13", "2026-06-12", "2026-06-11"}; !equalStrings(datesOf(got), want) {
		t.Errorf("windowed DESC = %v, want %v", datesOf(got), want)
	}

	// Unbounded since ("" until "2026-06-11") → 10, 11 DESC.
	got, err = repo.ListRange(ctx, "u1", "", "2026-06-11")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"2026-06-11", "2026-06-10"}; !equalStrings(datesOf(got), want) {
		t.Errorf("unbounded-since = %v, want %v", datesOf(got), want)
	}

	// Unbounded until ("2026-06-12" "") → 13, 12 DESC.
	got, err = repo.ListRange(ctx, "u1", "2026-06-12", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"2026-06-13", "2026-06-12"}; !equalStrings(datesOf(got), want) {
		t.Errorf("unbounded-until = %v, want %v", datesOf(got), want)
	}

	// Fully unbounded → all four for u1.
	got, err = repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("fully unbounded should return 4 rows, got %d", len(got))
	}

	// User isolation: u2 sees only their own row.
	got, err = repo.ListRange(ctx, "u2", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Date != "2026-06-11" {
		t.Errorf("u2 should see only their own row, got %+v", got)
	}
}

// --- DeleteBySleepID: removes match, idempotent when absent -------------

func TestDeleteBySleepID(t *testing.T) {
	ctx := context.Background()
	repo := NewSQLiteRepository(dbtest.New(t))
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Upsert(ctx, Entry{
		UserID: "u1", Date: "2026-06-14", RecoveryScore: fptr(70),
		CycleID: 1, SleepID: "sleep-x",
	}, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Delete a non-matching sleep_id → no error, row untouched.
	if err := repo.DeleteBySleepID(ctx, "u1", "sleep-absent"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	got, err := repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("row should survive non-matching delete, got %d", len(got))
	}

	// Cross-user delete must not touch u1's row.
	if err = repo.DeleteBySleepID(ctx, "u2", "sleep-x"); err != nil {
		t.Fatalf("cross-user delete: %v", err)
	}
	got, _ = repo.ListRange(ctx, "u1", "", "")
	if len(got) != 1 {
		t.Fatalf("cross-user delete leaked, u1 rows = %d", len(got))
	}

	// Matching delete removes the row.
	if err = repo.DeleteBySleepID(ctx, "u1", "sleep-x"); err != nil {
		t.Fatalf("delete match: %v", err)
	}
	got, err = repo.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("matching delete should remove the row, got %+v", got)
	}

	// Second delete of the same sleep_id → still no error (idempotent).
	if err = repo.DeleteBySleepID(ctx, "u1", "sleep-x"); err != nil {
		t.Errorf("idempotent delete: want nil, got %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
