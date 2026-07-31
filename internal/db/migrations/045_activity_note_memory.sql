-- migrations/045_activity_note_memory.sql
-- Admit endurance session notes (runs, hikes, walks, rides) as a third
-- distillation source. See prog-strength-docs/sows/running-session-notes.md
-- (widened to every endurance type when the unified model landed; path kept).
--
-- No new column: migration 042 already re-pointed source_workout_id at
-- activities(id), which since unification holds EVERY session type — so a run
-- note's provenance FK is the same column a lift note's is. All this migration
-- does is widen the discriminator CHECK to accept 'activity_note' alongside it.
-- SQLite cannot widen a table-level CHECK in place, hence the rebuild-and-copy;
-- id values are preserved so the vec_agent_memories join (memory_id) stays
-- valid and no vectors are rewritten, exactly as in 035 and 042.
--
-- One value for every non-strength type (not 'run_note' / 'hike_note' / ...):
-- the sport is recoverable by joining activities.activity_type, so denormalizing
-- it into the discriminator would only buy a rebuild per future activity type —
-- the churn the Go type registry exists to avoid.

CREATE TABLE agent_memories_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           TEXT NOT NULL,
    distilled_text    TEXT NOT NULL,
    source_type       TEXT NOT NULL,                                            -- 'chat_session' | 'workout_note' | 'activity_note'
    source_session_id TEXT REFERENCES chat_sessions(id) ON DELETE CASCADE,
    source_message_id INTEGER REFERENCES chat_messages(id) ON DELETE SET NULL,
    source_workout_id TEXT REFERENCES activities(id) ON DELETE CASCADE,         -- any activity: lift or endurance session
    embedding_model   TEXT NOT NULL,
    embedding_dim     INTEGER NOT NULL,
    superseded_at     DATETIME,
    created_at        DATETIME NOT NULL,
    CHECK (
        (source_type = 'chat_session' AND source_session_id IS NOT NULL AND source_workout_id IS NULL) OR
        (source_type IN ('workout_note', 'activity_note') AND source_workout_id IS NOT NULL AND source_session_id IS NULL)
    )
);

-- Copy verbatim: every existing row's source_type and FKs are already correct.
INSERT INTO agent_memories_new (id, user_id, distilled_text, source_type,
    source_session_id, source_message_id, source_workout_id,
    embedding_model, embedding_dim, superseded_at, created_at)
SELECT id, user_id, distilled_text, source_type,
    source_session_id, source_message_id, source_workout_id,
    embedding_model, embedding_dim, superseded_at, created_at
FROM agent_memories;

DROP TABLE agent_memories;
ALTER TABLE agent_memories_new RENAME TO agent_memories;

CREATE INDEX idx_agent_memories_user ON agent_memories(user_id);
CREATE INDEX idx_agent_memories_source_session ON agent_memories(source_session_id);
CREATE INDEX idx_agent_memories_source_workout ON agent_memories(source_workout_id);
