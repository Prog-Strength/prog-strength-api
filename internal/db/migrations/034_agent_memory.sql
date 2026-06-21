-- migrations/034_agent_memory.sql
-- Agent vector memory: durable, cross-conversation observations the
-- coach can recall. agent_memories holds the distilled text + provenance
-- (plain SQL, what the admin dump reads); vec_agent_memories holds the
-- float vectors in a sqlite-vec vec0 virtual table for per-user KNN.
-- Everything lives in app.db so the source link is a real FK and the
-- whole feature rides the existing Litestream -> S3 backup. See
-- prog-strength-docs/sows/agent-vector-memory.md.

CREATE TABLE agent_memories (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           TEXT NOT NULL,
    distilled_text    TEXT NOT NULL,
    source_session_id TEXT NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    source_message_id INTEGER REFERENCES chat_messages(id) ON DELETE SET NULL,
    embedding_model   TEXT NOT NULL,
    embedding_dim     INTEGER NOT NULL,
    superseded_at     DATETIME,
    created_at        DATETIME NOT NULL
);

-- Per-user scans for the dump + dedup probe; provenance/cascade lookups.
CREATE INDEX idx_agent_memories_user ON agent_memories(user_id);
CREATE INDEX idx_agent_memories_source_session ON agent_memories(source_session_id);

-- Float vectors. memory_id == agent_memories.id (join key). user_id is a
-- vec0 metadata column so KNN stays scoped to one user. distance_metric=
-- cosine matches the cosine distance_threshold the retrieval path gates on.
-- float[1536] == text-embedding-3-small; a model with a different dim is a
-- new table (see SOW Model-Version Migration), not an ALTER here.
CREATE VIRTUAL TABLE vec_agent_memories USING vec0(
    memory_id INTEGER PRIMARY KEY,
    user_id   TEXT,
    embedding float[1536] distance_metric=cosine
);

-- Distillation state. NULL = not yet distilled. The job's work-list is
-- "idle AND memory_distilled_at IS NULL"; set once a session is processed
-- (even when it yields zero observations) so it isn't re-examined.
ALTER TABLE chat_sessions ADD COLUMN memory_distilled_at DATETIME;
