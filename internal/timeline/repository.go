package timeline

import (
	"context"
	"time"
)

// Cursor is the keyset position for feed pagination: the (occurred_at, id)
// pair of the last post on a page. The next page asks for posts strictly
// before this position in (occurred_at DESC, id DESC) order. id is the
// tiebreaker that makes the cursor a total order so posts sharing an
// occurred_at paginate without gaps or repeats. The handler encodes it into
// the opaque `before`/`next_before` token; the repository works in this
// typed form.
type Cursor struct {
	OccurredAt time.Time
	ID         string
}

// Repository persists the feed index and its interactions. Implementations
// are in-memory (dev/test default) and SQLite (prod), mirroring the
// dual-implementation pattern of the other domains. All methods are
// context-first. Ownership/visibility is NOT enforced here — the repository
// returns rows by id and the handler's canView/canModerate split is the
// single authorization point (so the friends SOW changes one rule, not every
// query). The exception is ListFeed, which is inherently author-scoped.
type Repository interface {
	// EnsurePost idempotently inserts the feed-index row for ref and returns
	// it. On the UNIQUE(user_id, source_type, source_id) conflict it is a
	// no-op insert and returns the existing post, so the live write hook and
	// the backfill share one path and a re-run never double-posts.
	EnsurePost(ctx context.Context, ref PostRef) (Post, error)

	// ListFeed returns the author's posts newest-first (occurred_at DESC, id
	// DESC), capped at limit. When before != nil, only posts strictly before
	// that keyset position are returned. The returned cursor points at the
	// last post on the page for the next request, and is nil when the feed is
	// exhausted (fewer than limit rows remained).
	ListFeed(ctx context.Context, userID string, limit int, before *Cursor) ([]Post, *Cursor, error)

	// GetPost returns one post by id, or ErrNotFound. Visibility is the
	// handler's concern; this is a raw lookup.
	GetPost(ctx context.Context, id string) (Post, error)

	// AddComment inserts a flat comment by userID on postID and returns it.
	// The body is expected to be already validated (non-empty, <=2000 chars);
	// returns ErrNotFound if the post does not exist.
	AddComment(ctx context.Context, postID, userID, body string) (Comment, error)

	// DeleteComment soft-deletes the comment (stamps deleted_at), preserving
	// thread position. Ownership (canModerate) is checked by the handler.
	// Returns ErrNotFound when no live comment matches.
	DeleteComment(ctx context.Context, commentID string) error

	// ListComments returns a post's live comments oldest-first, excluding
	// soft-deleted rows.
	ListComments(ctx context.Context, postID string) ([]Comment, error)

	// AddReaction idempotently adds a reaction of type t by userID on postID
	// and returns it; on the UNIQUE(post_id, user_id, type) conflict it
	// returns the existing reaction. Returns ErrNotFound if the post does not
	// exist.
	AddReaction(ctx context.Context, postID, userID string, t ReactionType) (Reaction, error)

	// RemoveReaction removes the viewer's reaction of type t from postID. It
	// is idempotent: removing a reaction that isn't there is not an error.
	RemoveReaction(ctx context.Context, postID, userID string, t ReactionType) error

	// ReactionSummaries batch-loads the per-post reaction aggregate for a feed
	// page (counts per type, plus the viewer's own types in Mine), keyed by
	// post id. Posts with no reactions are absent from the map. Batched to
	// avoid an N+1 over the page.
	ReactionSummaries(ctx context.Context, postIDs []string, viewerID string) (map[string]ReactionSummary, error)

	// CommentCounts batch-loads the live (non-soft-deleted) comment count per
	// post for a feed page, keyed by post id. Posts with no comments are
	// absent from the map.
	CommentCounts(ctx context.Context, postIDs []string) (map[string]int, error)
}

// Publisher is the seam the source domains (workout, activity, PR) depend on
// to push events into the feed index, defined here and implemented in the
// server wiring layer so those domains never import the timeline package
// (avoiding an import cycle). It is a narrow projection of Repository.
// EnsurePost: publishing is best-effort, so callers log + meter the error and
// never surface it to the user — the backfill repairs any gap.
type Publisher interface {
	EnsurePost(ctx context.Context, ref PostRef) error
}

// SourceHydrator renders post content from the live source tables at read
// time. It is defined by the timeline package and implemented in the wiring
// layer as an adapter over the workout/activity/PR repositories, so the
// timeline domain never imports cross-domain internals. The map it returns is
// keyed by the same PostRef values it was handed, which is why PostRef is a
// comparable value type. Implementations batch one query per source_type per
// page to keep a feed page off the N+1 path.
type SourceHydrator interface {
	Hydrate(ctx context.Context, refs []PostRef) (map[PostRef]PostContent, error)
}
