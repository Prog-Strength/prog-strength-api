package follow

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// newMigratedDB opens a fresh migrated database in a temp dir, mirroring the
// timeline tests. Each test gets its own file so they run in parallel.
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

// backend names a Repository implementation for the table-driven contract.
type backend struct {
	name string
	repo Repository
}

// backends returns both implementations under common names. A fresh sqlite db
// and memory store are built per call so each subtest has isolated state.
func backends(t *testing.T) []backend {
	t.Helper()
	return []backend{
		{"memory", NewMemoryRepository()},
		{"sqlite", NewSQLiteRepository(newMigratedDB(t))},
	}
}

func ctx() context.Context { return context.Background() }

// --- state machine + guards ---------------------------------------------

func TestContract_RequestAcceptTransitions(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			f, err := b.repo.Request(ctx(), "a", "b")
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			if f.Status != StatusPending || f.AcceptedAt != nil || f.ID == "" {
				t.Fatalf("requested edge wrong: %+v", f)
			}

			// Accept by the followee (b) of the request authored by a.
			if err := b.repo.Accept(ctx(), "b", "a"); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			got, err := b.repo.Get(ctx(), "a", "b")
			if err != nil {
				t.Fatalf("Get after accept: %v", err)
			}
			if got.Status != StatusAccepted || got.AcceptedAt == nil {
				t.Fatalf("accepted edge wrong: %+v", got)
			}
		})
	}
}

func TestContract_SelfFollow(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			if _, err := b.repo.Request(ctx(), "a", "a"); !errors.Is(err, ErrSelfFollow) {
				t.Fatalf("Request self error = %v, want ErrSelfFollow", err)
			}
		})
	}
}

func TestContract_DuplicateRequest(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			if _, err := b.repo.Request(ctx(), "a", "b"); err != nil {
				t.Fatalf("Request: %v", err)
			}
			if _, err := b.repo.Request(ctx(), "a", "b"); !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("dup pending error = %v, want ErrAlreadyExists", err)
			}
			// Even after acceptance, a duplicate request is a conflict.
			if err := b.repo.Accept(ctx(), "b", "a"); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			if _, err := b.repo.Request(ctx(), "a", "b"); !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("dup accepted error = %v, want ErrAlreadyExists", err)
			}
		})
	}
}

func TestContract_RerequestAfterRejectAndCancel(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			// Reject frees the pair to be re-requested.
			if _, err := b.repo.Request(ctx(), "a", "b"); err != nil {
				t.Fatalf("Request 1: %v", err)
			}
			if err := b.repo.Reject(ctx(), "b", "a"); err != nil {
				t.Fatalf("Reject: %v", err)
			}
			if _, err := b.repo.Request(ctx(), "a", "b"); err != nil {
				t.Fatalf("Re-request after reject: %v", err)
			}
			// Cancel frees it again.
			if err := b.repo.Cancel(ctx(), "a", "b"); err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			if _, err := b.repo.Request(ctx(), "a", "b"); err != nil {
				t.Fatalf("Re-request after cancel: %v", err)
			}
		})
	}
}

func TestContract_AcceptNoPendingNotFound(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			if err := b.repo.Accept(ctx(), "b", "a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Accept missing = %v, want ErrNotFound", err)
			}
			// Accepting an already-accepted row is also not-found (no pending).
			if _, err := b.repo.Request(ctx(), "a", "b"); err != nil {
				t.Fatalf("Request: %v", err)
			}
			if err := b.repo.Accept(ctx(), "b", "a"); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			if err := b.repo.Accept(ctx(), "b", "a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("re-accept = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestContract_RejectCancelUnfollowRemoveNotFound(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			if err := b.repo.Reject(ctx(), "b", "a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Reject missing = %v, want ErrNotFound", err)
			}
			if err := b.repo.Cancel(ctx(), "a", "b"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Cancel missing = %v, want ErrNotFound", err)
			}
			if err := b.repo.Unfollow(ctx(), "a", "b"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Unfollow missing = %v, want ErrNotFound", err)
			}
			if err := b.repo.RemoveFollower(ctx(), "b", "a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("RemoveFollower missing = %v, want ErrNotFound", err)
			}
			// Unfollow only matches accepted rows: a pending row is not-found.
			if _, err := b.repo.Request(ctx(), "a", "b"); err != nil {
				t.Fatalf("Request: %v", err)
			}
			if err := b.repo.Unfollow(ctx(), "a", "b"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Unfollow pending = %v, want ErrNotFound", err)
			}
			if err := b.repo.RemoveFollower(ctx(), "b", "a"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("RemoveFollower pending = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestContract_UnfollowAndRemoveFollower(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			mustAccept(t, b.repo, "a", "b")

			// a unfollows b → edge gone.
			if err := b.repo.Unfollow(ctx(), "a", "b"); err != nil {
				t.Fatalf("Unfollow: %v", err)
			}
			if _, err := b.repo.Get(ctx(), "a", "b"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("edge should be gone, got %v", err)
			}

			// Re-establish, then b removes follower a.
			mustAccept(t, b.repo, "a", "b")
			if err := b.repo.RemoveFollower(ctx(), "b", "a"); err != nil {
				t.Fatalf("RemoveFollower: %v", err)
			}
			if _, err := b.repo.Get(ctx(), "a", "b"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("edge should be gone after remove, got %v", err)
			}
		})
	}
}

func TestContract_PendingCap(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			// Drive a's pending count to the cap.
			for i := 0; i < PendingCap; i++ {
				followee := "u" + itoa(i)
				if _, err := b.repo.Request(ctx(), "a", followee); err != nil {
					t.Fatalf("Request %d: %v", i, err)
				}
			}
			n, err := b.repo.CountPending(ctx(), "a")
			if err != nil {
				t.Fatalf("CountPending: %v", err)
			}
			if n != PendingCap {
				t.Fatalf("CountPending = %d, want %d", n, PendingCap)
			}
			if _, err := b.repo.Request(ctx(), "a", "over"); !errors.Is(err, ErrPendingCapExceeded) {
				t.Fatalf("over-cap error = %v, want ErrPendingCapExceeded", err)
			}
		})
	}
}

// --- counts + projections -----------------------------------------------

func TestContract_AcceptedFolloweesAndCounts(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			mustAccept(t, b.repo, "a", "x")
			mustAccept(t, b.repo, "a", "y")
			// A pending edge is excluded from accepted projections/counts.
			if _, err := b.repo.Request(ctx(), "a", "z"); err != nil {
				t.Fatalf("Request z: %v", err)
			}
			// Someone follows a (affects a's followers, not following).
			mustAccept(t, b.repo, "f", "a")

			followees, err := b.repo.AcceptedFollowees(ctx(), "a")
			if err != nil {
				t.Fatalf("AcceptedFollowees: %v", err)
			}
			if len(followees) != 2 || !contains(followees, "x") || !contains(followees, "y") {
				t.Fatalf("AcceptedFollowees = %v, want [x y]", followees)
			}

			following, err := b.repo.CountFollowing(ctx(), "a")
			if err != nil {
				t.Fatalf("CountFollowing: %v", err)
			}
			if following != 2 {
				t.Fatalf("CountFollowing = %d, want 2", following)
			}
			followers, err := b.repo.CountFollowers(ctx(), "a")
			if err != nil {
				t.Fatalf("CountFollowers: %v", err)
			}
			if followers != 1 {
				t.Fatalf("CountFollowers = %d, want 1", followers)
			}
		})
	}
}

// --- relationship -------------------------------------------------------

func TestContract_RelationshipAllValues(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			// self
			assertRel(t, b.repo, "a", "a", RelationshipSelf)
			// none
			assertRel(t, b.repo, "a", "b", RelationshipNone)
			// requested: a → c pending
			if _, err := b.repo.Request(ctx(), "a", "c"); err != nil {
				t.Fatalf("Request c: %v", err)
			}
			assertRel(t, b.repo, "a", "c", RelationshipRequested)
			// pending_incoming: d → a pending
			if _, err := b.repo.Request(ctx(), "d", "a"); err != nil {
				t.Fatalf("Request d: %v", err)
			}
			assertRel(t, b.repo, "a", "d", RelationshipPendingIncoming)
			// following: a → e accepted
			mustAccept(t, b.repo, "a", "e")
			assertRel(t, b.repo, "a", "e", RelationshipFollowing)
		})
	}
}

func TestContract_RelationshipsBatch(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			if _, err := b.repo.Request(ctx(), "a", "c"); err != nil {
				t.Fatalf("Request c: %v", err)
			}
			if _, err := b.repo.Request(ctx(), "d", "a"); err != nil {
				t.Fatalf("Request d: %v", err)
			}
			mustAccept(t, b.repo, "a", "e")

			ids := []string{"a", "b", "c", "d", "e"}
			rels, err := b.repo.Relationships(ctx(), "a", ids)
			if err != nil {
				t.Fatalf("Relationships: %v", err)
			}
			want := map[string]Relationship{
				"a": RelationshipSelf,
				"b": RelationshipNone,
				"c": RelationshipRequested,
				"d": RelationshipPendingIncoming,
				"e": RelationshipFollowing,
			}
			for id, w := range want {
				if rels[id] != w {
					t.Errorf("rel[%s] = %q, want %q", id, rels[id], w)
				}
			}
			if len(rels) != len(ids) {
				t.Errorf("rels len = %d, want %d (every id present)", len(rels), len(ids))
			}
		})
	}
}

// --- listing + pagination -----------------------------------------------

func TestContract_ListFollowersFollowingOrderingAndPagination(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			mr, isMem := b.repo.(*MemoryRepository)
			sr, _ := b.repo.(*SQLiteRepository)

			base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
			// Three accepted followers of "target", ascending created_at; newest
			// first should be f3, f2, f1.
			setNow(mr, sr, isMem, base)
			mustAccept(t, b.repo, "f1", "target")
			setNow(mr, sr, isMem, base.Add(time.Hour))
			mustAccept(t, b.repo, "f2", "target")
			setNow(mr, sr, isMem, base.Add(2*time.Hour))
			mustAccept(t, b.repo, "f3", "target")

			// Page 1: limit 2 → f3, f2 + cursor.
			page1, next, err := b.repo.ListFollowers(ctx(), "target", 2, nil)
			if err != nil {
				t.Fatalf("ListFollowers p1: %v", err)
			}
			if len(page1) != 2 || page1[0].FollowerID != "f3" || page1[1].FollowerID != "f2" {
				t.Fatalf("page1 = %v, want [f3 f2]", followerIDs(page1))
			}
			if next == nil {
				t.Fatal("page1 cursor should be non-nil")
			}
			// Page 2: → f1, nil cursor.
			page2, next2, err := b.repo.ListFollowers(ctx(), "target", 2, next)
			if err != nil {
				t.Fatalf("ListFollowers p2: %v", err)
			}
			if len(page2) != 1 || page2[0].FollowerID != "f1" {
				t.Fatalf("page2 = %v, want [f1]", followerIDs(page2))
			}
			if next2 != nil {
				t.Fatal("page2 cursor should be nil at end")
			}
		})
	}
}

func TestContract_ListWithCreatedAtTies(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			mr, isMem := b.repo.(*MemoryRepository)
			sr, _ := b.repo.(*SQLiteRepository)

			tie := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
			setNow(mr, sr, isMem, tie)
			// All share created_at; id is the tiebreaker (DESC).
			mustAccept(t, b.repo, "p1", "target")
			mustAccept(t, b.repo, "p2", "target")
			mustAccept(t, b.repo, "p3", "target")

			// Walk one row per page; the union must be all 3 with no repeats.
			seen := map[string]bool{}
			var cursor *Cursor
			for i := 0; i < 5; i++ {
				page, next, err := b.repo.ListFollowers(ctx(), "target", 1, cursor)
				if err != nil {
					t.Fatalf("page %d: %v", i, err)
				}
				if len(page) == 0 {
					break
				}
				for _, f := range page {
					if seen[f.FollowerID] {
						t.Fatalf("duplicate row %s across pages", f.FollowerID)
					}
					seen[f.FollowerID] = true
				}
				if next == nil {
					break
				}
				cursor = next
			}
			if len(seen) != 3 {
				t.Fatalf("paged rows = %v, want all 3", seen)
			}
		})
	}
}

func TestContract_ListIncomingOutgoingRequests(t *testing.T) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			// Incoming to "me": x → me, y → me.
			if _, err := b.repo.Request(ctx(), "x", "me"); err != nil {
				t.Fatalf("Request x: %v", err)
			}
			if _, err := b.repo.Request(ctx(), "y", "me"); err != nil {
				t.Fatalf("Request y: %v", err)
			}
			// Outgoing from "me": me → p.
			if _, err := b.repo.Request(ctx(), "me", "p"); err != nil {
				t.Fatalf("Request p: %v", err)
			}
			// An accepted edge must not show up in either inbox.
			mustAccept(t, b.repo, "me", "q")

			incoming, _, err := b.repo.ListIncomingRequests(ctx(), "me", 10, nil)
			if err != nil {
				t.Fatalf("ListIncomingRequests: %v", err)
			}
			if len(incoming) != 2 {
				t.Fatalf("incoming len = %d, want 2", len(incoming))
			}
			for _, f := range incoming {
				if f.FolloweeID != "me" || f.Status != StatusPending {
					t.Fatalf("incoming row wrong: %+v", f)
				}
			}

			outgoing, _, err := b.repo.ListOutgoingRequests(ctx(), "me", 10, nil)
			if err != nil {
				t.Fatalf("ListOutgoingRequests: %v", err)
			}
			if len(outgoing) != 1 || outgoing[0].FolloweeID != "p" {
				t.Fatalf("outgoing = %v, want [p]", followeeIDs(outgoing))
			}
		})
	}
}

func TestContract_CursorRoundTripParity(t *testing.T) {
	// Both backends must return identical cursors for the same data, so a
	// cursor minted by one paginates correctly through the other's codec.
	in := Cursor{CreatedAt: time.Date(2026, 6, 1, 12, 34, 56, 789, time.UTC), ID: "edge-1"}
	out, err := decodeCursor(encodeCursor(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.CreatedAt.Equal(in.CreatedAt) || out.ID != in.ID {
		t.Fatalf("round-trip = %+v, want %+v", out, in)
	}
}

// --- helpers ------------------------------------------------------------

// mustAccept requests follower → followee and accepts it, failing the test on
// any error.
func mustAccept(t *testing.T, repo Repository, follower, followee string) {
	t.Helper()
	if _, err := repo.Request(ctx(), follower, followee); err != nil {
		t.Fatalf("Request %s→%s: %v", follower, followee, err)
	}
	if err := repo.Accept(ctx(), followee, follower); err != nil {
		t.Fatalf("Accept %s→%s: %v", follower, followee, err)
	}
}

// setNow pins the clock on whichever backend is active so created_at ordering
// is deterministic.
func setNow(mr *MemoryRepository, sr *SQLiteRepository, isMem bool, at time.Time) {
	if isMem {
		mr.now = func() time.Time { return at }
		return
	}
	sr.now = func() time.Time { return at }
}

func assertRel(t *testing.T, repo Repository, viewer, other string, want Relationship) {
	t.Helper()
	got, err := repo.Relationship(ctx(), viewer, other)
	if err != nil {
		t.Fatalf("Relationship(%s,%s): %v", viewer, other, err)
	}
	if got != want {
		t.Fatalf("Relationship(%s,%s) = %q, want %q", viewer, other, got, want)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func followerIDs(fs []Follow) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.FollowerID
	}
	return out
}

func followeeIDs(fs []Follow) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.FolloweeID
	}
	return out
}

// itoa is a tiny base-10 formatter avoiding a strconv import in the cap loop.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
