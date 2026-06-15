package timeline

import "time"

// Post is one entry in the feed index — a thin pointer to a training event,
// not its content. It records WHICH event it points at (SourceType +
// SourceID) and WHEN it happened (OccurredAt); the card's title, metrics,
// and link are hydrated from the live source tables at read time, so a post
// always reflects the current state of its underlying workout or run. Stable
// post identity (ID) is what comments and reactions foreign-key to.
type Post struct {
	ID     string
	UserID string // the author (whose training this is)
	// SourceType + SourceID identify the underlying record in its source
	// domain. (UserID, SourceType, SourceID) is unique, which makes
	// EnsurePost idempotent.
	SourceType SourceType
	SourceID   string
	// OccurredAt is the event's natural time (workout date, run start, PR
	// achievement date). It drives feed ordering, deliberately distinct from
	// CreatedAt (when the index row was written, which the backfill makes
	// "now" for old events).
	OccurredAt time.Time
	// Visibility is forward-scaffolding for the friends SOW: always
	// VisibilityPrivate in v1. See the authorization split in the handler.
	Visibility Visibility
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Comment is a flat comment on a post. Comments are soft-deleted (DeletedAt)
// rather than removed so thread positions and future moderation survive;
// reads exclude soft-deleted rows.
type Comment struct {
	ID        string
	PostID    string
	UserID    string // the commenter
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
	// DeletedAt is nil for a live comment, set on soft delete.
	DeletedAt *time.Time
}

// Reaction is one typed reaction by one user on one post. A user may stack
// distinct types on a post (Slack-style) but not duplicate a type; the
// (PostID, UserID, Type) uniqueness enforces that and makes adds idempotent.
type Reaction struct {
	ID        string
	PostID    string
	UserID    string
	Type      ReactionType
	CreatedAt time.Time
}

// PostRef is the identity-and-time tuple the Publisher and SourceHydrator
// pass around — everything needed to ensure a post exists and to fetch its
// content, without the post's storage id. It is a comparable value type so
// the hydrator can return map[PostRef]PostContent keyed by the refs it was
// asked to render (all fields are comparable; OccurredAt is a time.Time,
// which is comparable).
type PostRef struct {
	UserID     string
	SourceType SourceType
	SourceID   string
	OccurredAt time.Time
}

// PostContent is the rendered, platform-agnostic card content a hydrator
// produces from a source record: a Title (e.g. "Push day"), a Subtitle
// summary, a small set of Metrics chips (e.g. "12 sets · 8,400 lb"), and an
// Href deep-link to the existing source detail page. Never stored — always
// rendered from the live source, so source edits reflect in the feed with no
// post-update machinery.
type PostContent struct {
	Title    string
	Subtitle string
	Metrics  []string
	Href     string
}

// ReactionSummary is the per-post reaction aggregate a feed page carries:
// Counts holds the total per type, and Mine lists the viewer's own reaction
// types (so the UI can render the active state of each reaction button).
type ReactionSummary struct {
	Counts map[ReactionType]int
	Mine   []ReactionType
}
