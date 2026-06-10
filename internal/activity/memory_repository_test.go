package activity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The MemoryRepository must behave identically to the SQLite one on the
// paths the handler relies on. These cover the in-memory-specific risks:
// defensive copies, the emulated UNIQUE check, and the Create rollback
// (nothing stored) when the archiver fails.

func TestMemory_CreateAndGet(t *testing.T) {
	t.Parallel()
	arch := NewMemoryArchiver()
	repo := NewMemoryRepository(arch)
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("tcx")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// New Hive-partitioned key scheme.
	wantPrefix := "user_id=u1/activity_type=running/year=2026/month=06/day=01/"
	if !strings.HasPrefix(a.TCXS3Key, wantPrefix) || !strings.HasSuffix(a.TCXS3Key, ".tcx") {
		t.Fatalf("TCXS3Key = %q, want prefix %q and .tcx suffix", a.TCXS3Key, wantPrefix)
	}
	if _, err := arch.Get(context.Background(), a.TCXS3Key); err != nil {
		t.Fatalf("archiver missing object: %v", err)
	}
	// Metadata stamp survived the round-trip.
	meta, ok := arch.Meta(a.TCXS3Key)
	if !ok {
		t.Fatal("archiver missing metadata")
	}
	if meta.IngestSource != IngestManualTCX {
		t.Errorf("meta.IngestSource = %q, want %q", meta.IngestSource, IngestManualTCX)
	}

	got, err := repo.Get(ctx, "u1", a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Trackpoints) != 2 {
		t.Fatalf("want 2 trackpoints, got %d", len(got.Trackpoints))
	}
	// Mutating the returned copy must not corrupt stored state.
	got.Trackpoints[0].Sequence = 99
	again, _ := repo.Get(ctx, "u1", a.ID)
	if again.Trackpoints[0].Sequence != 0 {
		t.Fatal("defensive copy violated: caller mutated internal state")
	}
}

func TestMemory_Duplicate(t *testing.T) {
	t.Parallel()
	repo := NewMemoryRepository(NewMemoryArchiver())
	ctx := context.Background()

	a1 := newActivity("u1", IngestManualTCX, "dup", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a1, []byte("a")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	a2 := newActivity("u1", IngestManualTCX, "dup", mustTime(t, "2026-06-02T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a2, []byte("b")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}

	// Same source_activity_id from a DIFFERENT ingest source is NOT a
	// duplicate: the dedup key is per-source. (Today only manual_tcx is
	// wired up; this assertion locks in the contract for the future API
	// source.)
	a3 := newActivity("u1", IngestGarminAPI, "dup", mustTime(t, "2026-06-03T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a3, []byte("c")); err != nil {
		t.Fatalf("cross-source same activity id should succeed: %v", err)
	}
}

func TestMemory_ArchiverFailureStoresNothing(t *testing.T) {
	t.Parallel()
	arch := NewMemoryArchiver()
	arch.PutErr = errors.New("s3 down")
	repo := NewMemoryRepository(arch)
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("x")); !errors.Is(err, ErrStorage) {
		t.Fatalf("want ErrStorage, got %v", err)
	}
	if got, _ := repo.List(ctx, "u1", 10, nil); len(got) != 0 {
		t.Fatalf("nothing should be stored, got %d", len(got))
	}
}

func TestMemory_OwnershipAndSoftDelete(t *testing.T) {
	t.Parallel()
	repo := NewMemoryRepository(NewMemoryArchiver())
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.Get(ctx, "u2", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Get: want ErrNotFound, got %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", a.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := repo.Get(ctx, "u1", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted Get: want ErrNotFound, got %v", err)
	}
}
