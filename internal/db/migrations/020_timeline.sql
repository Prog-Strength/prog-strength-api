-- migrations/020_timeline.sql
-- The Timeline feed index and its interaction tables.
--
-- The feed is a hybrid feed-index: rather than copying workout/run/PR
-- content into a feed table (which drifts when the source is edited) or
-- UNION-ing heterogeneous source tables at read time (which makes post
-- identity and pagination painful), timeline_post stores only WHICH event
-- a post points at (source_type + source_id) and WHEN it happened
-- (occurred_at). Card content is hydrated from the live source tables at
-- read time, so a post always reflects the current state of its workout or
-- run. Comments and reactions foreign-key to the stable timeline_post.id.
--
-- Feed ordering is a single keyset scan on (user_id, occurred_at DESC, id DESC):
-- the covering index below serves "this user's posts, newest first" with
-- the trailing id column making the cursor a total order (occurred_at ties
-- break on id). This is exactly the shape the planned friends feed needs —
-- the only change there is widening the WHERE from `user_id = :viewer` to
-- `user_id IN (:followees)`, no schema migration.
--
-- visibility is forward-scaffolding for that social SOW: present and
-- CHECK-constrained from day one, always 'private' in v1. The friends SOW
-- flips defaults and adds 'friends'/'public' semantics without backfilling
-- existing rows.
--
-- Timestamps are DATETIME to match every surrounding migration (see
-- 016_activity_best_efforts.sql / 018_nutrition_lookup_cache.sql); SQLite
-- stores them as ISO-8601 text and the repo's time scanning assumes that.

CREATE TABLE timeline_post (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,              -- the post author (whose training this is)
    source_type TEXT NOT NULL,              -- which source domain the post points at
    source_id   TEXT NOT NULL,              -- id of the underlying record in that domain
    occurred_at DATETIME NOT NULL,          -- the event's natural time; drives feed ordering
    visibility  TEXT NOT NULL DEFAULT 'private',  -- forward-scaffolding; always 'private' in v1
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    CHECK(source_type IN ('workout', 'run', 'pr', 'best_effort')),
    CHECK(visibility IN ('private', 'friends', 'public')),
    -- Makes EnsurePost idempotent and prevents a duplicate post for the
    -- same training event (the live write hook and the backfill both insert
    -- through this key, so a re-run can never double-post).
    UNIQUE(user_id, source_type, source_id)
);

-- Serves the keyset feed query: this user's posts, newest first, with id
-- as the tiebreaker so the (occurred_at, id) cursor is a total order.
CREATE INDEX idx_timeline_post_feed
    ON timeline_post(user_id, occurred_at DESC, id DESC);

CREATE TABLE timeline_comment (
    id         TEXT PRIMARY KEY,
    post_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,               -- the commenter
    body       TEXT NOT NULL,               -- validated non-empty, <=2000 chars in the domain
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME,                     -- soft delete: preserves thread positions / future moderation
    FOREIGN KEY (post_id) REFERENCES timeline_post(id) ON DELETE CASCADE
);

-- Comments are listed oldest-first under a post; this index serves that
-- scan and the per-post comment-count aggregate.
CREATE INDEX idx_timeline_comment_post
    ON timeline_comment(post_id, created_at);

CREATE TABLE timeline_reaction (
    id         TEXT PRIMARY KEY,
    post_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    type       TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    CHECK(type IN ('like', 'strong', 'fire', 'celebrate')),
    -- A user may stack distinct reaction types on one post but not duplicate
    -- a type; this also makes AddReaction idempotent.
    UNIQUE(post_id, user_id, type),
    FOREIGN KEY (post_id) REFERENCES timeline_post(id) ON DELETE CASCADE
);

-- Serves the per-post reaction-summary aggregate for a feed page.
CREATE INDEX idx_timeline_reaction_post
    ON timeline_reaction(post_id);
