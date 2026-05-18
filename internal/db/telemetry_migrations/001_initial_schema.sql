-- telemetry_migrations/001_initial_schema.sql
-- Initial schema for telemetry.db, the separate SQLite file that
-- holds agent runtime telemetry. Owned by the Go API (the API
-- process opens both /data/app.db and /data/telemetry.db via
-- different *sql.DB handles).
--
-- See prog-strength-docs/sows/monitoring-and-observability.md for
-- the full design.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- One row per /chat request handled by the agent service.
CREATE TABLE IF NOT EXISTS agent_turns (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    -- Groups turns within a single conversation. Generated client-side
    -- when the chat is first opened; server falls back to a generated
    -- UUID if absent.
    session_id TEXT NOT NULL,
    -- Model that handled the main turn, e.g. claude-sonnet-4-6.
    model TEXT NOT NULL,
    -- 'simple' or 'complex' — the model router's classification.
    routed_tier TEXT NOT NULL,
    -- Model used by the router itself for classification.
    router_model TEXT NOT NULL,
    router_latency_ms INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    total_latency_ms INTEGER NOT NULL,
    -- Time to first SSE event from the model.
    time_to_first_token_ms INTEGER NOT NULL,
    -- 'end_turn', 'tool_use', 'max_tokens', 'error'.
    completion_reason TEXT NOT NULL,
    -- Populated only when completion_reason = 'error'.
    error TEXT,
    started_at DATETIME NOT NULL,
    ended_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE INDEX idx_agent_turns_started
    ON agent_turns(started_at DESC);
CREATE INDEX idx_agent_turns_user_started
    ON agent_turns(user_id, started_at DESC);
-- The dominant query for the future chat-history UI: every turn in
-- this conversation, oldest first.
CREATE INDEX idx_agent_turns_user_session_started
    ON agent_turns(user_id, session_id, started_at);

-- One row per MCP tool invocation made during a turn.
CREATE TABLE IF NOT EXISTS agent_tool_calls (
    id TEXT PRIMARY KEY,
    turn_id TEXT NOT NULL REFERENCES agent_turns(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    -- Full tool input as JSON. NULL'd by the daily TTL job after 90
    -- days; metadata (tool_name, latency, error) stays forever.
    arguments_json TEXT,
    -- First N characters of the tool's response, or NULL on error or
    -- after TTL expiry.
    result_summary TEXT,
    latency_ms INTEGER NOT NULL,
    error TEXT,
    started_at DATETIME NOT NULL,
    ended_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE INDEX idx_agent_tool_calls_turn
    ON agent_tool_calls(turn_id);

-- One row per user or assistant message. Only table that stores the
-- actual conversation content.
CREATE TABLE IF NOT EXISTS agent_messages (
    id TEXT PRIMARY KEY,
    turn_id TEXT NOT NULL REFERENCES agent_turns(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
    -- Message text. NULL'd by the daily TTL job after 90 days.
    content TEXT,
    token_count INTEGER,
    created_at DATETIME NOT NULL
);

CREATE INDEX idx_agent_messages_turn_created
    ON agent_messages(turn_id, created_at);
