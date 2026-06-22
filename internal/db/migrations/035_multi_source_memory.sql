-- migrations/035_multi_source_memory.sql
-- Multi-Source Agent Memory: generalize agent_memories provenance from a
-- chat-only FK to a source_type discriminator + typed per-source FK columns.
-- SQLite cannot relax NOT NULL or add a table CHECK in place, so the table is
-- rebuilt-and-copied; id values are preserved so the vec_agent_memories join
-- (memory_id) stays valid and no vectors are rewritten. See
-- prog-strength-docs/sows/multi-source-agent-memory.md.

CREATE TABLE agent_memories_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           TEXT NOT NULL,
    distilled_text    TEXT NOT NULL,
    source_type       TEXT NOT NULL,                                          -- 'chat_session' | 'workout_note'
    source_session_id TEXT REFERENCES chat_sessions(id) ON DELETE CASCADE,    -- nullable now
    source_message_id INTEGER REFERENCES chat_messages(id) ON DELETE SET NULL,
    source_workout_id TEXT REFERENCES workouts(id) ON DELETE CASCADE,         -- new
    embedding_model   TEXT NOT NULL,
    embedding_dim     INTEGER NOT NULL,
    superseded_at     DATETIME,
    created_at        DATETIME NOT NULL,
    CHECK (
        (source_type = 'chat_session' AND source_session_id IS NOT NULL AND source_workout_id IS NULL) OR
        (source_type = 'workout_note' AND source_workout_id IS NOT NULL AND source_session_id IS NULL)
    )
);

-- Existing rows are all chat-sourced.
INSERT INTO agent_memories_new (id, user_id, distilled_text, source_type,
    source_session_id, source_message_id, source_workout_id,
    embedding_model, embedding_dim, superseded_at, created_at)
SELECT id, user_id, distilled_text, 'chat_session',
    source_session_id, source_message_id, NULL,
    embedding_model, embedding_dim, superseded_at, created_at
FROM agent_memories;

DROP TABLE agent_memories;
ALTER TABLE agent_memories_new RENAME TO agent_memories;

-- Recreate the 034 indexes and add one for the new workout FK (cascade lookups).
CREATE INDEX idx_agent_memories_user ON agent_memories(user_id);
CREATE INDEX idx_agent_memories_source_session ON agent_memories(source_session_id);
CREATE INDEX idx_agent_memories_source_workout ON agent_memories(source_workout_id);

-- Workout distillation marker. NULL = not yet distilled.
ALTER TABLE workouts ADD COLUMN memory_distilled_at DATETIME;
