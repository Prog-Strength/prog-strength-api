package timeline

import (
	"context"
	"errors"
	"testing"
)

// The MemoryRepository must behave identically to the SQLite one — the
// handler tests run against it. These reuse the shared assertions defined in
// sqlite_repository_test.go so both backends are held to the same semantics,
// plus a couple of memory-specific checks (defensive copies, emulated
// cascade).

func newMemoryRepo() *MemoryRepository { return NewMemoryRepository() }

func TestMemory_EnsurePostIdempotent(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()

	first, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2026-06-01T07:00:00Z")))
	if err != nil {
		t.Fatalf("first EnsurePost: %v", err)
	}
	second, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2027-01-01T00:00:00Z")))
	if err != nil {
		t.Fatalf("second EnsurePost: %v", err)
	}
	if second.ID != first.ID || !second.OccurredAt.Equal(first.OccurredAt) {
		t.Fatalf("idempotency broken: %+v vs %+v", first, second)
	}
	if first.Visibility != VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", first.Visibility)
	}
}

func TestMemory_ListFeedKeysetPagination(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()
	seedFeed(t, repo, ctx)
	assertFeedPagination(t, repo, ctx)
}

func TestMemory_ReactionStackingAndToggle(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()
	assertReactionStacking(t, repo, ctx)
}

func TestMemory_SoftDeleteExclusion(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()
	assertSoftDeleteExclusion(t, repo, ctx)
}

func TestMemory_BatchSummariesAndCounts(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()
	assertBatchSummariesAndCounts(t, repo, ctx)
}

func TestMemory_Validation(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()
	assertValidation(t, repo, ctx)
}

func TestMemory_GetPostNotFound(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()
	if _, err := repo.GetPost(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMemory_AddOnMissingPost(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()
	assertAddOnMissingPost(t, repo, ctx)
}

// TestMemory_CascadeOnPostDelete exercises the emulated ON DELETE CASCADE.
// The Repository interface has no DeletePost (post lifecycle is the source
// domain's concern), so the test reaches for the unexported deletePost helper
// that the memory repo provides specifically to mirror the SQLite foreign
// key's cascade.
func TestMemory_CascadeOnPostDelete(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()

	post, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2026-06-01T07:00:00Z")))
	if err != nil {
		t.Fatalf("EnsurePost: %v", err)
	}
	if _, err := repo.AddComment(ctx, post.ID, "u2", "nice work"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, err := repo.AddReaction(ctx, post.ID, "u2", ReactionFire); err != nil {
		t.Fatalf("AddReaction: %v", err)
	}

	repo.deletePost(post.ID)

	if _, err := repo.GetPost(ctx, post.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post should be gone: %v", err)
	}
	live, err := repo.ListComments(ctx, post.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("comments should cascade away, got %d", len(live))
	}
	sums, err := repo.ReactionSummaries(ctx, []string{post.ID}, "u2")
	if err != nil {
		t.Fatalf("ReactionSummaries: %v", err)
	}
	if _, ok := sums[post.ID]; ok {
		t.Fatalf("reactions should cascade away")
	}
}

// TestMemory_DefensiveCopies verifies a caller mutating a returned comment
// cannot corrupt stored state — the memory-specific risk the SQLite backend
// doesn't have.
func TestMemory_DefensiveCopies(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ctx := context.Background()

	post, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2026-06-01T07:00:00Z")))
	if err != nil {
		t.Fatalf("EnsurePost: %v", err)
	}
	if _, err := repo.AddComment(ctx, post.ID, "u2", "original"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	got, err := repo.ListComments(ctx, post.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	got[0].Body = "mutated"

	again, err := repo.ListComments(ctx, post.ID)
	if err != nil {
		t.Fatalf("ListComments again: %v", err)
	}
	if again[0].Body != "original" {
		t.Fatalf("defensive copy violated: stored body = %q", again[0].Body)
	}
}
