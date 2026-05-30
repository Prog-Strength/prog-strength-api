-- migrations/008_chat_sessions.sql
-- Persistent chat session storage. Today the API has no chat
-- persistence — the agent is stateless and every conversation lives
-- only in the client's memory. This migration adds the two tables
-- the persistent-chat-sessions SOW calls for so clients can browse
-- past conversations, resume them, and let the API evict the
-- oldest when the per-user cap is hit.
--
-- chat_sessions: one row per conversation. The id is a UUID minted
-- client-side and passed to POST /chat-sessions, mirroring the
-- workouts table's "id from caller" convention. user_id has no FK
-- (also matching workouts) because OAuth-only users aren't always
-- in the DB before their first write.
--
-- title is empty until the LLM-titling roundtrip (agent /title +
-- API PATCH) lands; the UI renders a placeholder during that
-- ~1s gap. Capped at 80 chars to match the PATCH validator.
--
-- last_message_at is the eviction sort key: when the per-user
-- count would exceed the cap, the row with the oldest
-- last_message_at gets hard-deleted (CASCADE on messages).
-- User-initiated DELETE sets deleted_at instead, so a future
-- restore-from-trash feature is cheap to bolt on.

CREATE TABLE IF NOT EXISTS chat_sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    last_message_at DATETIME NOT NULL,
    deleted_at DATETIME
);

-- One partial index serves both the history list query
-- (ORDER BY last_message_at DESC for the active user) and the
-- eviction picker (oldest last_message_at for that user). SQLite
-- can use a DESC index for both directions of an ORDER BY, so
-- this one index covers both paths.
CREATE INDEX idx_chat_sessions_user_recent
    ON chat_sessions(user_id, last_message_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_chat_sessions_deleted_at ON chat_sessions(deleted_at);

-- chat_messages: one row per turn-side (user or assistant). The
-- two-rows-per-turn shape matches Anthropic's role enum and lets
-- future features (retry an assistant reply, mid-turn edit) land
-- without a schema rewrite.
--
-- position is the explicit order within a session, assigned at
-- append time as max(position) + 1. We rely on position rather
-- than created_at because two messages can share a wall-clock
-- second (user message + immediate assistant response), and
-- "order by created_at, id" leans on the autoincrement, which is
-- a fragile invariant to bake into a read query.
--
-- model is set on assistant rows (e.g. "claude-sonnet-4-6") so
-- the history view can surface "via Sonnet" badges the same way
-- live chat does today. Null on user rows.
--
-- tools_json is an opaque JSON blob of any tool calls the
-- assistant emitted during this turn. The chat UI renders these
-- as hint chips ("agent called list_workouts"); storing the JSON
-- avoids a normalized tool_calls table for v1 — see the SOW's
-- open question about lifting this into a table if/when an
-- analytical query needs it.
--
-- ON DELETE CASCADE so the eviction path can hard-delete a
-- chat_sessions row and trust SQLite to remove the messages in
-- the same statement.

CREATE TABLE IF NOT EXISTS chat_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
    content TEXT NOT NULL,
    model TEXT,
    tools_json TEXT,
    created_at DATETIME NOT NULL
);

CREATE UNIQUE INDEX idx_chat_messages_session_position
    ON chat_messages(session_id, position);
