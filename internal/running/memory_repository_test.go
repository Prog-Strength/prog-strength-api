package running

import (
	"context"
	"errors"
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

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("tcx")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := arch.Get("runs/u1/" + s.ID + ".tcx"); !ok {
		t.Fatal("archiver missing object")
	}

	got, err := repo.Get(ctx, "u1", s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Trackpoints) != 2 {
		t.Fatalf("want 2 trackpoints, got %d", len(got.Trackpoints))
	}
	// Mutating the returned copy must not corrupt stored state.
	got.Trackpoints[0].Sequence = 99
	again, _ := repo.Get(ctx, "u1", s.ID)
	if again.Trackpoints[0].Sequence != 0 {
		t.Fatal("defensive copy violated: caller mutated internal state")
	}
}

func TestMemory_Duplicate(t *testing.T) {
	t.Parallel()
	repo := NewMemoryRepository(NewMemoryArchiver())
	ctx := context.Background()

	s1 := newSession("u1", "dup", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s1, []byte("a")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	s2 := newSession("u1", "dup", mustTime(t, "2026-06-02T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s2, []byte("b")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestMemory_ArchiverFailureStoresNothing(t *testing.T) {
	t.Parallel()
	arch := NewMemoryArchiver()
	arch.PutErr = errors.New("s3 down")
	repo := NewMemoryRepository(arch)
	ctx := context.Background()

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("x")); !errors.Is(err, ErrStorage) {
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

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.Get(ctx, "u2", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Get: want ErrNotFound, got %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", s.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := repo.Get(ctx, "u1", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted Get: want ErrNotFound, got %v", err)
	}
}
