-- migrations/046_timeline_all_activity_types.sql
-- Collapse the per-sport timeline source types into one `activity` value so
-- every registered activity type reaches the social feed. Fixes: a hike was
-- unpostable because the CHECK below only admitted 'workout' and 'run'.
--
-- The old CHECK — CHECK(source_type IN ('workout','run','pr','best_effort'))
-- from migration 020 — made the feed's discriminator a *sport* taxonomy, so
-- every new activity type cost a table rebuild. That is the same trap
-- migration 042 removed from activities.activity_type, and the reason
-- prog-strength-docs/adding-an-activity-type.md had to list "timeline post
-- publishing" under "What does NOT come free". After this migration the
-- source type names the source *domain* (a session / a PR event / a best
-- effort), the sport is read from activities.activity_type via the Go type
-- registry, and a new type is a descriptor + registration with no schema
-- change at all.
--
-- 'workout' and 'run' rows both become 'activity'. That cannot collide on
-- UNIQUE(user_id, source_type, source_id): since 042 preserved workout ids
-- as activity ids, source_id is an id from the one activities table and a
-- row has exactly one type. A plain INSERT is used so a collision would
-- abort the transaction rather than silently drop a post — the same
-- id-disjointness assertion 042 makes.
--
-- timeline_comment and timeline_reaction are rebuilt against
-- timeline_post_new BEFORE the old parent is dropped. That is not optional:
-- DROP TABLE performs an implicit DELETE and defer_foreign_keys defers
-- constraint *checking* but NOT the ON DELETE CASCADE *action*, so dropping
-- the parent with live child FKs would silently cascade every comment and
-- reaction away (the lesson documented in migrations 033 and 042). The
-- final RENAME of timeline_post_new -> timeline_post rewrites both rebuilt
-- children's FK target name.

PRAGMA defer_foreign_keys = 1;

-- 1. New feed index, identical to 020 except for the widened source_type
--    domain. Ids are preserved verbatim so comments and reactions re-attach
--    to the same posts.
CREATE TABLE timeline_post_new (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    source_type TEXT NOT NULL,              -- which source domain the post points at
    source_id   TEXT NOT NULL,              -- id of the underlying record in that domain
    occurred_at DATETIME NOT NULL,          -- the event's natural time; drives feed ordering
    visibility  TEXT NOT NULL DEFAULT 'private',
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    CHECK(source_type IN ('activity', 'pr', 'best_effort')),
    CHECK(visibility IN ('private', 'friends', 'public')),
    UNIQUE(user_id, source_type, source_id)
);

INSERT INTO timeline_post_new (id, user_id, source_type, source_id, occurred_at,
    visibility, created_at, updated_at)
SELECT id, user_id,
    CASE WHEN source_type IN ('workout', 'run') THEN 'activity' ELSE source_type END,
    source_id, occurred_at, visibility, created_at, updated_at
FROM timeline_post;

-- 2. Children rebuilt against the new parent, rows copied verbatim.
CREATE TABLE timeline_comment_new (
    id         TEXT PRIMARY KEY,
    post_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    body       TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME,
    FOREIGN KEY (post_id) REFERENCES timeline_post_new(id) ON DELETE CASCADE
);

INSERT INTO timeline_comment_new (id, post_id, user_id, body, created_at, updated_at, deleted_at)
SELECT id, post_id, user_id, body, created_at, updated_at, deleted_at FROM timeline_comment;

CREATE TABLE timeline_reaction_new (
    id         TEXT PRIMARY KEY,
    post_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    type       TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    CHECK(type IN ('like', 'strong', 'fire', 'celebrate')),
    UNIQUE(post_id, user_id, type),
    FOREIGN KEY (post_id) REFERENCES timeline_post_new(id) ON DELETE CASCADE
);

INSERT INTO timeline_reaction_new (id, post_id, user_id, type, created_at)
SELECT id, post_id, user_id, type, created_at FROM timeline_reaction;

-- 3. Drop the children first (they still point at the old parent, so the
--    parent's implicit DELETE has nothing left to cascade into), then the
--    parent, then rename. The parent rename rewrites the new children's FK
--    target from timeline_post_new to timeline_post.
DROP TABLE timeline_comment;
DROP TABLE timeline_reaction;
DROP TABLE timeline_post;

ALTER TABLE timeline_post_new RENAME TO timeline_post;
ALTER TABLE timeline_comment_new RENAME TO timeline_comment;
ALTER TABLE timeline_reaction_new RENAME TO timeline_reaction;

-- 4. Indexes are dropped with their tables; recreate them exactly as 020 had
--    them (the keyset feed scan, the comment thread scan, the reaction
--    aggregate).
CREATE INDEX idx_timeline_post_feed
    ON timeline_post(user_id, occurred_at DESC, id DESC);
CREATE INDEX idx_timeline_comment_post
    ON timeline_comment(post_id, created_at);
CREATE INDEX idx_timeline_reaction_post
    ON timeline_reaction(post_id);
