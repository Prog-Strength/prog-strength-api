package timeline

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// newMigratedDB opens a fresh migrated database in a temp dir with foreign
// keys on (so ON DELETE CASCADE fires), mirroring the activity tests. Each
// test gets its own file so they run in parallel without sharing state.
func newMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn
}

func newSQLiteRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	return NewSQLiteRepository(newMigratedDB(t))
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt
}

func ref(userID, sourceID string, occurred time.Time) PostRef {
	return PostRef{
		UserID:     userID,
		SourceType: SourceWorkout,
		SourceID:   sourceID,
		OccurredAt: occurred,
	}
}

// --- EnsurePost idempotency --------------------------------------------------

func TestSQLite_EnsurePostIdempotent(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()

	first, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2026-06-01T07:00:00Z")))
	if err != nil {
		t.Fatalf("first EnsurePost: %v", err)
	}
	if first.ID == "" {
		t.Fatal("expected generated id")
	}
	if first.Visibility != VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", first.Visibility)
	}

	// Re-ensuring the same (user, source_type, source_id) — even with a
	// different occurred_at — must return the original row unchanged.
	second, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2027-01-01T00:00:00Z")))
	if err != nil {
		t.Fatalf("second EnsurePost: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("idempotency broken: id %s != %s", second.ID, first.ID)
	}
	if !second.OccurredAt.Equal(first.OccurredAt) {
		t.Fatalf("occurred_at changed on conflict: %v != %v", second.OccurredAt, first.OccurredAt)
	}

	var count int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM timeline_post`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("want 1 row after re-ensure, got %d", count)
	}
}

// --- Feed keyset ordering + pagination --------------------------------------

func TestSQLite_ListFeedKeysetPagination(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()
	seedFeed(t, repo, ctx)
	assertFeedPagination(t, repo, ctx)
}

// --- Reactions: UNIQUE stacking + toggle + idempotency ----------------------

func TestSQLite_ReactionStackingAndToggle(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()
	assertReactionStacking(t, repo, ctx)
}

// --- Cascade: deleting a post removes its comments + reactions ---------------

func TestSQLite_CascadeOnPostDelete(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
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

	// The interface has no DeletePost (post lifecycle belongs to the source
	// domain), so exercise the schema's ON DELETE CASCADE with a raw delete
	// against the test db.
	if _, err := repo.db.ExecContext(ctx, `DELETE FROM timeline_post WHERE id = ?`, post.ID); err != nil {
		t.Fatalf("raw delete: %v", err)
	}

	var comments, reactions int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM timeline_comment WHERE post_id = ?`, post.ID).Scan(&comments); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM timeline_reaction WHERE post_id = ?`, post.ID).Scan(&reactions); err != nil {
		t.Fatalf("count reactions: %v", err)
	}
	if comments != 0 || reactions != 0 {
		t.Fatalf("cascade failed: comments=%d reactions=%d", comments, reactions)
	}
}

// --- Soft-delete exclusion ---------------------------------------------------

func TestSQLite_SoftDeleteExclusion(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()
	assertSoftDeleteExclusion(t, repo, ctx)
}

// --- Batch summaries + counts ------------------------------------------------

func TestSQLite_BatchSummariesAndCounts(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()
	assertBatchSummariesAndCounts(t, repo, ctx)
}

// --- Validation --------------------------------------------------------------

func TestSQLite_Validation(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()
	assertValidation(t, repo, ctx)
}

// --- GetPost not found -------------------------------------------------------

func TestSQLite_GetPostNotFound(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()
	if _, err := repo.GetPost(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// --- Add comment/reaction on a missing post ---------------------------------

func TestSQLite_AddOnMissingPost(t *testing.T) {
	t.Parallel()
	repo := newSQLiteRepo(t)
	ctx := context.Background()
	assertAddOnMissingPost(t, repo, ctx)
}

// ============================================================================
// Shared assertions, run against BOTH backends (SQLite here, Memory in the
// other file) so the two implementations are held to identical semantics.
// ============================================================================

// seedFeed inserts six posts for u1 (plus one for u2 that must never appear),
// including an occurred_at tie broken by id, and is used by the pagination
// assertion below.
func seedFeed(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	// Three distinct times; two posts share the middle time to exercise the
	// id tiebreaker.
	t1 := mustTime(t, "2026-06-01T07:00:00Z")
	t2 := mustTime(t, "2026-06-02T07:00:00Z")
	t3 := mustTime(t, "2026-06-03T07:00:00Z")
	for _, r := range []PostRef{
		ref("u1", "a", t1),
		ref("u1", "b", t2),
		ref("u1", "c", t2), // tie with b on occurred_at
		ref("u1", "d", t3),
		ref("u2", "x", t3), // other user — must be excluded
	} {
		if _, err := repo.EnsurePost(ctx, r); err != nil {
			t.Fatalf("EnsurePost %s: %v", r.SourceID, err)
		}
	}
}

func assertFeedPagination(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()

	// Full feed, newest-first, u2's post excluded.
	all, cur, err := repo.ListFeed(ctx, "u1", 10, nil)
	if err != nil {
		t.Fatalf("ListFeed all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("want 4 posts for u1, got %d", len(all))
	}
	if cur != nil {
		t.Fatalf("exhausted feed should return nil cursor, got %+v", cur)
	}
	// Verify strict newest-first total order (occurred_at DESC, id DESC).
	for i := 1; i < len(all); i++ {
		prev, next := all[i-1], all[i]
		if next.OccurredAt.After(prev.OccurredAt) {
			t.Fatalf("not newest-first at %d: %v then %v", i, prev.OccurredAt, next.OccurredAt)
		}
		if next.OccurredAt.Equal(prev.OccurredAt) && next.ID >= prev.ID {
			t.Fatalf("tie not broken by id DESC at %d: %s then %s", i, prev.ID, next.ID)
		}
	}

	// Paginate two at a time and confirm we walk the same total order with no
	// gaps or repeats, and that the final page yields a nil cursor.
	var paged []Post
	var before *Cursor
	for {
		page, next, err := repo.ListFeed(ctx, "u1", 2, before)
		if err != nil {
			t.Fatalf("ListFeed page: %v", err)
		}
		paged = append(paged, page...)
		if next == nil {
			break
		}
		before = next
		if len(paged) > 4 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(paged) != len(all) {
		t.Fatalf("paged %d posts, want %d", len(paged), len(all))
	}
	for i := range all {
		if paged[i].ID != all[i].ID {
			t.Fatalf("page order diverged at %d: %s != %s", i, paged[i].ID, all[i].ID)
		}
	}
}

func assertReactionStacking(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	post, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2026-06-01T07:00:00Z")))
	if err != nil {
		t.Fatalf("EnsurePost: %v", err)
	}

	// One user stacks two distinct types.
	r1, err := repo.AddReaction(ctx, post.ID, "u2", ReactionStrong)
	if err != nil {
		t.Fatalf("AddReaction strong: %v", err)
	}
	if _, err := repo.AddReaction(ctx, post.ID, "u2", ReactionFire); err != nil {
		t.Fatalf("AddReaction fire: %v", err)
	}

	// Re-adding 'strong' is idempotent: same row back, still one of that type.
	again, err := repo.AddReaction(ctx, post.ID, "u2", ReactionStrong)
	if err != nil {
		t.Fatalf("AddReaction strong again: %v", err)
	}
	if again.ID != r1.ID {
		t.Fatalf("idempotent add returned new row: %s != %s", again.ID, r1.ID)
	}

	sum, err := repo.ReactionSummaries(ctx, []string{post.ID}, "u2")
	if err != nil {
		t.Fatalf("ReactionSummaries: %v", err)
	}
	s := sum[post.ID]
	if s.Counts[ReactionStrong] != 1 || s.Counts[ReactionFire] != 1 {
		t.Fatalf("counts wrong after stacking: %+v", s.Counts)
	}
	if len(s.Mine) != 2 {
		t.Fatalf("Mine should have 2 types, got %v", s.Mine)
	}

	// Toggle off 'strong'; 'fire' remains.
	if err := repo.RemoveReaction(ctx, post.ID, "u2", ReactionStrong); err != nil {
		t.Fatalf("RemoveReaction strong: %v", err)
	}
	// Removing the now-absent 'strong' again is not an error.
	if err := repo.RemoveReaction(ctx, post.ID, "u2", ReactionStrong); err != nil {
		t.Fatalf("idempotent RemoveReaction: %v", err)
	}
	sum, err = repo.ReactionSummaries(ctx, []string{post.ID}, "u2")
	if err != nil {
		t.Fatalf("ReactionSummaries after remove: %v", err)
	}
	s = sum[post.ID]
	if _, ok := s.Counts[ReactionStrong]; ok {
		t.Fatalf("strong should be gone, counts=%+v", s.Counts)
	}
	if s.Counts[ReactionFire] != 1 {
		t.Fatalf("fire should remain, counts=%+v", s.Counts)
	}
	if len(s.Mine) != 1 || s.Mine[0] != ReactionFire {
		t.Fatalf("Mine wrong after toggle: %v", s.Mine)
	}
}

func assertSoftDeleteExclusion(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	post, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2026-06-01T07:00:00Z")))
	if err != nil {
		t.Fatalf("EnsurePost: %v", err)
	}
	keep, err := repo.AddComment(ctx, post.ID, "u2", "first")
	if err != nil {
		t.Fatalf("AddComment keep: %v", err)
	}
	gone, err := repo.AddComment(ctx, post.ID, "u3", "second")
	if err != nil {
		t.Fatalf("AddComment gone: %v", err)
	}

	if err := repo.DeleteComment(ctx, gone.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	// Double-delete is ErrNotFound (no live comment).
	if err := repo.DeleteComment(ctx, gone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}

	live, err := repo.ListComments(ctx, post.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(live) != 1 || live[0].ID != keep.ID {
		t.Fatalf("deleted comment not excluded: %+v", live)
	}

	counts, err := repo.CommentCounts(ctx, []string{post.ID})
	if err != nil {
		t.Fatalf("CommentCounts: %v", err)
	}
	if counts[post.ID] != 1 {
		t.Fatalf("count should exclude soft-deleted: %d", counts[post.ID])
	}
}

func assertBatchSummariesAndCounts(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	p1, _ := repo.EnsurePost(ctx, ref("u1", "p1", mustTime(t, "2026-06-01T07:00:00Z")))
	p2, _ := repo.EnsurePost(ctx, ref("u1", "p2", mustTime(t, "2026-06-02T07:00:00Z")))
	p3, _ := repo.EnsurePost(ctx, ref("u1", "p3", mustTime(t, "2026-06-03T07:00:00Z"))) // zero of everything

	// p1: viewer u1 likes; u2 likes + fires. p2: only u2 celebrates.
	mustReact(t, repo, ctx, p1.ID, "u1", ReactionLike)
	mustReact(t, repo, ctx, p1.ID, "u2", ReactionLike)
	mustReact(t, repo, ctx, p1.ID, "u2", ReactionFire)
	mustReact(t, repo, ctx, p2.ID, "u2", ReactionCelebrate)

	// p1: two live comments + one deleted. p2: one comment.
	mustComment(t, repo, ctx, p1.ID, "u2", "a")
	mustComment(t, repo, ctx, p1.ID, "u3", "b")
	del := mustComment(t, repo, ctx, p1.ID, "u4", "c")
	if err := repo.DeleteComment(ctx, del.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	mustComment(t, repo, ctx, p2.ID, "u2", "d")

	ids := []string{p1.ID, p2.ID, p3.ID}

	sums, err := repo.ReactionSummaries(ctx, ids, "u1")
	if err != nil {
		t.Fatalf("ReactionSummaries: %v", err)
	}
	if sums[p1.ID].Counts[ReactionLike] != 2 || sums[p1.ID].Counts[ReactionFire] != 1 {
		t.Fatalf("p1 counts wrong: %+v", sums[p1.ID].Counts)
	}
	// Viewer u1 only liked p1.
	if mine := sums[p1.ID].Mine; len(mine) != 1 || mine[0] != ReactionLike {
		t.Fatalf("p1 Mine wrong for u1: %v", mine)
	}
	if sums[p2.ID].Counts[ReactionCelebrate] != 1 {
		t.Fatalf("p2 counts wrong: %+v", sums[p2.ID].Counts)
	}
	if len(sums[p2.ID].Mine) != 0 {
		t.Fatalf("p2 Mine should be empty for u1: %v", sums[p2.ID].Mine)
	}
	if _, ok := sums[p3.ID]; ok {
		t.Fatalf("p3 has no reactions, should be absent from map")
	}

	counts, err := repo.CommentCounts(ctx, ids)
	if err != nil {
		t.Fatalf("CommentCounts: %v", err)
	}
	if counts[p1.ID] != 2 {
		t.Fatalf("p1 live comment count = %d, want 2", counts[p1.ID])
	}
	if counts[p2.ID] != 1 {
		t.Fatalf("p2 comment count = %d, want 1", counts[p2.ID])
	}
	if _, ok := counts[p3.ID]; ok {
		t.Fatalf("p3 has no comments, should be absent from map")
	}
}

func assertValidation(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	post, err := repo.EnsurePost(ctx, ref("u1", "w1", mustTime(t, "2026-06-01T07:00:00Z")))
	if err != nil {
		t.Fatalf("EnsurePost: %v", err)
	}

	for _, body := range []string{"", "   ", "\t\n  "} {
		if _, err := repo.AddComment(ctx, post.ID, "u2", body); !errors.Is(err, ErrValidation) {
			t.Fatalf("empty body %q: want ErrValidation, got %v", body, err)
		}
	}
	if _, err := repo.AddComment(ctx, post.ID, "u2", strings.Repeat("x", 2001)); !errors.Is(err, ErrValidation) {
		t.Fatalf("over-long body: want ErrValidation, got %v", err)
	}
	// A 2000-char body is exactly at the limit and accepted.
	if _, err := repo.AddComment(ctx, post.ID, "u2", strings.Repeat("x", 2000)); err != nil {
		t.Fatalf("2000-char body should be accepted: %v", err)
	}

	if _, err := repo.AddReaction(ctx, post.ID, "u2", ReactionType("bogus")); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid reaction type: want ErrValidation, got %v", err)
	}
}

// assertAddOnMissingPost holds both backends to the Repository contract that
// AddComment/AddReaction against a non-existent post id return ErrNotFound. It
// also pins the validate-input-first-then-existence order: an invalid body /
// reaction type on a missing post surfaces ErrValidation, not ErrNotFound.
func assertAddOnMissingPost(t *testing.T, repo Repository, ctx context.Context) {
	t.Helper()
	const missing = "no-such-post"

	if _, err := repo.AddComment(ctx, missing, "u1", "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddComment on missing post: want ErrNotFound, got %v", err)
	}
	if _, err := repo.AddReaction(ctx, missing, "u1", ReactionFire); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddReaction on missing post: want ErrNotFound, got %v", err)
	}

	// Validation runs before the existence check, so a bad input on a missing
	// post is ErrValidation in both backends.
	if _, err := repo.AddComment(ctx, missing, "u1", "  "); !errors.Is(err, ErrValidation) {
		t.Fatalf("AddComment empty body on missing post: want ErrValidation, got %v", err)
	}
	if _, err := repo.AddReaction(ctx, missing, "u1", ReactionType("bogus")); !errors.Is(err, ErrValidation) {
		t.Fatalf("AddReaction bad type on missing post: want ErrValidation, got %v", err)
	}
}

func mustReact(t *testing.T, repo Repository, ctx context.Context, postID, userID string, ty ReactionType) {
	t.Helper()
	if _, err := repo.AddReaction(ctx, postID, userID, ty); err != nil {
		t.Fatalf("AddReaction(%s,%s): %v", userID, ty, err)
	}
}

func mustComment(t *testing.T, repo Repository, ctx context.Context, postID, userID, body string) Comment {
	t.Helper()
	c, err := repo.AddComment(ctx, postID, userID, body)
	if err != nil {
		t.Fatalf("AddComment(%s): %v", userID, err)
	}
	return c
}
