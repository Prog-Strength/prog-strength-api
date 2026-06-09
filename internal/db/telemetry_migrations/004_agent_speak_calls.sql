-- telemetry_migrations/004_agent_speak_calls.sql
-- Records per-TTS-call character usage so the cost engine can price the
-- voice surface alongside the LLM surface (agent_turns). Today /speak is
-- fire-and-forget with no telemetry row; this table is the missing half
-- of the per-user daily-spend computation in internal/usage.
--
-- session_id is nullable: /speak is sometimes called outside a chat
-- session context, so we can't require it. error is nullable; a non-null
-- value means OpenAI rejected the call — we still record the row so a
-- caller cannot escape the cap by always failing.
--
-- No foreign key to users: telemetry.db is a physically separate SQLite
-- file from app.db and the two are intentionally decoupled. The
-- (user_id, started_at) index mirrors the shape agent_turns already has
-- so the daily-spend SUMs scan symmetrically across both tables.

CREATE TABLE IF NOT EXISTS agent_speak_calls (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    session_id TEXT,
    model TEXT NOT NULL,
    chars INTEGER NOT NULL,
    voice TEXT NOT NULL,
    started_at DATETIME NOT NULL,
    ended_at DATETIME NOT NULL,
    error TEXT
);

CREATE INDEX IF NOT EXISTS idx_agent_speak_calls_user_started
    ON agent_speak_calls(user_id, started_at);
