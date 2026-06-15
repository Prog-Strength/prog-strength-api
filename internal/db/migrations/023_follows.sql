-- migrations/022_follows.sql
-- The follow graph: a directed (follower → followee) edge with an explicit
-- request/accept state machine.
--
-- A single row models the entire relationship for an ordered pair: a follow
-- starts life as status='pending' when the follower requests, and flips to
-- 'accepted' (stamping accepted_at) when the followee accepts. Reject, cancel,
-- unfollow, and remove-follower all DELETE the row — there is no soft state to
-- preserve, and deleting frees the pair to be re-requested later (bounded only
-- by the pending-request cap enforced in the domain). The UNIQUE(follower_id,
-- followee_id) constraint is what makes Request idempotent-by-rejection: a
-- second request for the same ordered pair hits the constraint and surfaces as
-- the domain's ErrAlreadyExists.
--
-- CHECK(follower_id <> followee_id) is a storage-side backstop for the
-- self-follow guard the domain enforces first; CHECK(status IN (...)) pins the
-- two-state machine at the schema level.
--
-- The two covering indexes serve the graph's hot reads, both filtered by
-- status:
--   * idx_follows_followee_status — "this user's accepted followers" and the
--     incoming requests inbox (pending rows addressed to a followee).
--   * idx_follows_follower_status — "who this user follows" (the accepted-
--     followee feed projection) and the outgoing requests list.
--
-- Timestamps are DATETIME to match every surrounding migration; SQLite stores
-- them as ISO-8601 text and the repo's time scanning assumes that. accepted_at
-- is nullable: it is NULL while pending and stamped on acceptance.

CREATE TABLE follows (
    id          TEXT PRIMARY KEY,
    follower_id TEXT NOT NULL,              -- who initiated the follow
    followee_id TEXT NOT NULL,              -- who is being followed
    status      TEXT NOT NULL,              -- 'pending' | 'accepted'
    created_at  DATETIME NOT NULL,
    accepted_at DATETIME,                   -- NULL until the followee accepts
    CHECK (status IN ('pending','accepted')),
    CHECK (follower_id <> followee_id),
    -- One row per ordered pair: makes Request reject duplicates and lets a
    -- deleted (rejected/cancelled/unfollowed) pair be re-requested.
    UNIQUE (follower_id, followee_id)
);

-- Serves "accepted followers of X" and the incoming requests inbox.
CREATE INDEX idx_follows_followee_status ON follows(followee_id, status);
-- Serves the accepted-followee feed projection and the outgoing requests list.
CREATE INDEX idx_follows_follower_status ON follows(follower_id, status);
