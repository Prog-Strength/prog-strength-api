package beta

import (
	"context"
	"testing"
)

func TestSeedFromEnv_EmptyTableSeedsWithSentinel(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	n, err := r.SeedFromEnv(ctx, []string{"A@Example.com", "b@example.com"})
	if err != nil {
		t.Fatalf("SeedFromEnv: %v", err)
	}
	if n != 2 {
		t.Fatalf("seeded count = %d, want 2", n)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	for _, e := range list {
		if e.AddedBy == nil || *e.AddedBy != SeedAddedBy {
			t.Fatalf("added_by = %v, want sentinel %q", e.AddedBy, SeedAddedBy)
		}
	}

	// Normalized on the way in.
	allowed, err := r.IsAllowed(ctx, "a@example.com")
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("seeded email not normalized / not allowed")
	}
}

func TestSeedFromEnv_NonEmptyTableUntouched(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	// Pre-populate with an admin-added row.
	if err := r.Add(ctx, "admin-added@example.com", "operator@example.com", "manual"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	n, err := r.SeedFromEnv(ctx, []string{"env@example.com"})
	if err != nil {
		t.Fatalf("SeedFromEnv: %v", err)
	}
	if n != 0 {
		t.Fatalf("seeded count = %d, want 0 (table non-empty)", n)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Email != "admin-added@example.com" {
		t.Fatalf("seed overwrote existing rows: %+v", list)
	}
}

func TestSeedFromEnv_EmptyEnvNoOp(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	n, err := r.SeedFromEnv(ctx, nil)
	if err != nil {
		t.Fatalf("SeedFromEnv(nil): %v", err)
	}
	if n != 0 {
		t.Fatalf("seeded count = %d, want 0 for empty env", n)
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List len = %d, want 0", len(list))
	}
}

func TestSeedFromEnv_SecondBootDoesNotReseed(t *testing.T) {
	ctx := context.Background()
	r := newSQLiteBetaRepo(t)

	emails := []string{"one@example.com", "two@example.com"}
	if _, err := r.SeedFromEnv(ctx, emails); err != nil {
		t.Fatalf("SeedFromEnv first: %v", err)
	}
	// Second boot: table is now non-empty, so this is a guarded no-op.
	n, err := r.SeedFromEnv(ctx, emails)
	if err != nil {
		t.Fatalf("SeedFromEnv second: %v", err)
	}
	if n != 0 {
		t.Fatalf("second seed count = %d, want 0", n)
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2 (no duplicates)", len(list))
	}
}
