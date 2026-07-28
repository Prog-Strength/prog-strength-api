-- migrations/042_unified_activity_model.sql
-- Unify lifting workouts and endurance activities into one base table with
-- per-type detail tables. See prog-strength-docs/sows/unified-activity-model.md.
--
-- Id preservation is the invariant: workout ids become activity ids verbatim,
-- so PRs, 1RM history, timeline posts, and planned-workout completions never
-- re-point ids — only FK targets are rebuilt. The INSERT of workout rows into
-- the new base uses plain INSERT: a PK collision with an existing activity id
-- aborts the whole transaction, which IS the id-disjointness assertion.
--
-- Every table whose FK names `workouts` or the old `activities` is rebuilt
-- against activities_new BEFORE the old parents are dropped. That is not
-- optional: DROP TABLE performs an implicit DELETE, and defer_foreign_keys
-- defers constraint *checking* but NOT the ON DELETE CASCADE *action* (the
-- lesson documented in migration 033), so dropping a parent with live child
-- FKs would silently cascade the children away. Affected children:
-- activity_trackpoints / activity_best_efforts (-> activities),
-- workout_exercises / personal_records / personal_record_events /
-- exercise_one_rep_max_history / agent_memories.source_workout_id
-- (-> workouts). The final RENAME of activities_new -> activities rewrites
-- every rebuilt child's FK target name.

PRAGMA defer_foreign_keys = 1;

-- 1. New base table (no CHECK on activity_type / ingest_source: the Go type
--    registry owns valid values — SQLite can't widen a CHECK without another
--    rebuild). memory_distilled_at carries over from workouts (migration 035);
--    endurance rows start NULL, matching "not yet distilled".
CREATE TABLE activities_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    activity_type TEXT NOT NULL,
    start_time DATETIME NOT NULL,
    duration_seconds INTEGER,
    name TEXT,
    notes TEXT,
    avg_heart_rate_bpm INTEGER,
    max_heart_rate_bpm INTEGER,
    total_calories INTEGER,
    ingest_source TEXT NOT NULL,
    source_activity_id TEXT,
    tcx_s3_key TEXT,
    memory_distilled_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);

-- 2. Carry every existing activity row EXCEPT the strength_training
--    enrichment rows that are the linked target of a workout (those fold into
--    the workout's own row in step 3). The old table had no updated_at or
--    notes; updated_at seeds from created_at.
INSERT INTO activities_new (
    id, user_id, activity_type, start_time, duration_seconds, name, notes,
    avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
    ingest_source, source_activity_id, tcx_s3_key, memory_distilled_at,
    created_at, updated_at, deleted_at
)
SELECT id, user_id, activity_type, start_time, duration_seconds, name, NULL,
       avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
       ingest_source, source_activity_id, tcx_s3_key, NULL,
       created_at, created_at, deleted_at
FROM activities
WHERE id NOT IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

-- 3. Lift every workout into the base, folding its linked enrichment row
--    (vitals, tcx provenance, duration) when one exists. Ids are reused.
--    Duration precedence: enrichment duration wins, else ended_at −
--    performed_at, else NULL (in-progress / end-less lift).
INSERT INTO activities_new (
    id, user_id, activity_type, start_time, duration_seconds, name, notes,
    avg_heart_rate_bpm, max_heart_rate_bpm, total_calories,
    ingest_source, source_activity_id, tcx_s3_key, memory_distilled_at,
    created_at, updated_at, deleted_at
)
SELECT w.id, w.user_id, 'strength_training', w.performed_at,
       CASE
         WHEN a.duration_seconds IS NOT NULL THEN a.duration_seconds
         WHEN w.ended_at IS NOT NULL
           THEN CAST(ROUND((julianday(w.ended_at) - julianday(w.performed_at)) * 86400) AS INTEGER)
         ELSE NULL
       END,
       NULLIF(w.name, ''), NULLIF(w.notes, ''),
       a.avg_heart_rate_bpm, a.max_heart_rate_bpm, a.total_calories,
       COALESCE(a.ingest_source, 'manual'), a.source_activity_id, a.tcx_s3_key,
       w.memory_distilled_at, w.created_at, w.updated_at, w.deleted_at
FROM workouts w
LEFT JOIN activities a ON a.id = w.activity_id;

-- 4. Re-key folded enrichment trackpoints to the workout's id (still on the
--    OLD trackpoints table; the FK check is deferred to COMMIT, by which
--    point the rebuilt table resolves against the new base).
UPDATE activity_trackpoints
SET activity_id = (SELECT w.id FROM workouts w WHERE w.activity_id = activity_trackpoints.activity_id)
WHERE activity_id IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

-- 5. Rebuild activity_trackpoints / activity_best_efforts against the new
--    base (schema unchanged; post-037/038 column set) so dropping the old
--    activities table cannot cascade into them.
CREATE TABLE activity_trackpoints_new (
    activity_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    elapsed_seconds INTEGER NOT NULL,
    distance_meters REAL NOT NULL,
    heart_rate_bpm INTEGER,
    pace_sec_per_km REAL,
    elevation_meters REAL,
    latitude REAL,
    longitude REAL,
    PRIMARY KEY (activity_id, sequence),
    FOREIGN KEY (activity_id) REFERENCES activities_new(id) ON DELETE CASCADE
);
INSERT INTO activity_trackpoints_new (
    activity_id, sequence, elapsed_seconds, distance_meters,
    heart_rate_bpm, pace_sec_per_km, elevation_meters, latitude, longitude
)
SELECT activity_id, sequence, elapsed_seconds, distance_meters,
       heart_rate_bpm, pace_sec_per_km, elevation_meters, latitude, longitude
FROM activity_trackpoints;

CREATE TABLE activity_best_efforts_new (
    activity_id TEXT NOT NULL,
    distance_key TEXT NOT NULL,
    duration_seconds REAL NOT NULL,
    window_start_elapsed_seconds REAL,
    window_end_elapsed_seconds REAL,
    PRIMARY KEY (activity_id, distance_key),
    FOREIGN KEY (activity_id) REFERENCES activities_new(id) ON DELETE CASCADE,
    CHECK(distance_key IN ('1mi', '2mi', '5k', '10k', 'half_marathon', 'marathon'))
);
INSERT INTO activity_best_efforts_new (
    activity_id, distance_key, duration_seconds,
    window_start_elapsed_seconds, window_end_elapsed_seconds
)
SELECT activity_id, distance_key, duration_seconds,
       window_start_elapsed_seconds, window_end_elapsed_seconds
FROM activity_best_efforts;

-- 6. Endurance columns move into per-type detail tables (from the OLD
--    activities table; folded enrichment rows are excluded by the same NOT IN
--    used in step 2). Identical DDL per type — accepted duplication; Go
--    shares one store implementation parameterized by table name.
CREATE TABLE activity_run_details (
    activity_id TEXT PRIMARY KEY REFERENCES activities_new(id) ON DELETE CASCADE,
    distance_meters REAL NOT NULL,
    raw_distance_meters REAL NOT NULL DEFAULT 0,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    elevation_gain_meters REAL,
    environment TEXT NOT NULL DEFAULT 'outdoor' CHECK(environment IN ('outdoor','indoor')),
    route_geojson TEXT
);
INSERT INTO activity_run_details
SELECT id, distance_meters, raw_distance_meters, avg_pace_sec_per_km,
       best_pace_sec_per_km, elevation_gain_meters, environment, route_geojson
FROM activities WHERE activity_type = 'running'
  AND id NOT IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

CREATE TABLE activity_walk_details (
    activity_id TEXT PRIMARY KEY REFERENCES activities_new(id) ON DELETE CASCADE,
    distance_meters REAL NOT NULL,
    raw_distance_meters REAL NOT NULL DEFAULT 0,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    elevation_gain_meters REAL,
    environment TEXT NOT NULL DEFAULT 'outdoor' CHECK(environment IN ('outdoor','indoor')),
    route_geojson TEXT
);
INSERT INTO activity_walk_details
SELECT id, distance_meters, raw_distance_meters, avg_pace_sec_per_km,
       best_pace_sec_per_km, elevation_gain_meters, environment, route_geojson
FROM activities WHERE activity_type = 'walking'
  AND id NOT IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

CREATE TABLE activity_cycle_details (
    activity_id TEXT PRIMARY KEY REFERENCES activities_new(id) ON DELETE CASCADE,
    distance_meters REAL NOT NULL,
    raw_distance_meters REAL NOT NULL DEFAULT 0,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    elevation_gain_meters REAL,
    environment TEXT NOT NULL DEFAULT 'outdoor' CHECK(environment IN ('outdoor','indoor')),
    route_geojson TEXT
);
INSERT INTO activity_cycle_details
SELECT id, distance_meters, raw_distance_meters, avg_pace_sec_per_km,
       best_pace_sec_per_km, elevation_gain_meters, environment, route_geojson
FROM activities WHERE activity_type = 'cycling'
  AND id NOT IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

CREATE TABLE activity_other_details (
    activity_id TEXT PRIMARY KEY REFERENCES activities_new(id) ON DELETE CASCADE,
    distance_meters REAL NOT NULL,
    raw_distance_meters REAL NOT NULL DEFAULT 0,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    elevation_gain_meters REAL,
    environment TEXT NOT NULL DEFAULT 'outdoor' CHECK(environment IN ('outdoor','indoor')),
    route_geojson TEXT
);
INSERT INTO activity_other_details
SELECT id, distance_meters, raw_distance_meters, avg_pace_sec_per_km,
       best_pace_sec_per_km, elevation_gain_meters, environment, route_geojson
FROM activities WHERE activity_type = 'other'
  AND id NOT IN (SELECT activity_id FROM workouts WHERE activity_id IS NOT NULL);

-- 7. Strength children: rename + re-point. Integer ids are preserved. sets is
--    rebuilt because its FK clause names workout_exercises.
CREATE TABLE activity_exercises (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_id TEXT NOT NULL REFERENCES activities_new(id) ON DELETE CASCADE,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    exercise_order INTEGER NOT NULL,
    notes TEXT,
    superset_group INTEGER
);
INSERT INTO activity_exercises (id, activity_id, exercise_id, exercise_order, notes, superset_group)
SELECT id, workout_id, exercise_id, exercise_order, notes, superset_group FROM workout_exercises;
CREATE INDEX idx_activity_exercises_activity_id ON activity_exercises(activity_id);

CREATE TABLE sets_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_exercise_id INTEGER NOT NULL REFERENCES activity_exercises(id) ON DELETE CASCADE,
    reps INTEGER NOT NULL CHECK(reps > 0),
    weight REAL NOT NULL CHECK(weight >= 0),
    unit TEXT NOT NULL CHECK(unit IN ('lb','kg')),
    set_order INTEGER NOT NULL
);
INSERT INTO sets_new (id, activity_exercise_id, reps, weight, unit, set_order)
SELECT id, workout_exercise_id, reps, weight, unit, set_order FROM sets;
DROP TABLE sets;
ALTER TABLE sets_new RENAME TO sets;
CREATE INDEX idx_sets_activity_exercise_id ON sets(activity_exercise_id);
DROP TABLE workout_exercises;

-- 8. Re-point the strength-stat FKs: workout_id -> activity_id (ids
--    unchanged, rows identical). DDL copied from migrations 003/004.
CREATE TABLE exercise_one_rep_max_history_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    activity_id TEXT NOT NULL REFERENCES activities_new(id) ON DELETE CASCADE,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    performed_at DATETIME NOT NULL,
    min_estimated_1rm REAL NOT NULL,
    avg_estimated_1rm REAL NOT NULL,
    max_estimated_1rm REAL NOT NULL,
    set_count INTEGER NOT NULL CHECK(set_count > 0),
    unit TEXT NOT NULL CHECK(unit IN ('lb', 'kg')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
INSERT INTO exercise_one_rep_max_history_new (
    id, user_id, activity_id, exercise_id, performed_at,
    min_estimated_1rm, avg_estimated_1rm, max_estimated_1rm,
    set_count, unit, created_at, updated_at
)
SELECT id, user_id, workout_id, exercise_id, performed_at,
       min_estimated_1rm, avg_estimated_1rm, max_estimated_1rm,
       set_count, unit, created_at, updated_at
FROM exercise_one_rep_max_history;
DROP TABLE exercise_one_rep_max_history;
ALTER TABLE exercise_one_rep_max_history_new RENAME TO exercise_one_rep_max_history;
CREATE INDEX idx_orm_history_user_exercise_time
    ON exercise_one_rep_max_history(user_id, exercise_id, performed_at DESC);
CREATE INDEX idx_orm_history_activity
    ON exercise_one_rep_max_history(activity_id);

CREATE TABLE personal_records_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    activity_id TEXT NOT NULL REFERENCES activities_new(id) ON DELETE CASCADE,
    weight REAL NOT NULL CHECK(weight > 0),
    reps INTEGER NOT NULL CHECK(reps >= 1),
    unit TEXT NOT NULL CHECK(unit IN ('lb', 'kg')),
    achieved_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE(user_id, exercise_id)
);
INSERT INTO personal_records_new (
    id, user_id, exercise_id, activity_id, weight, reps, unit,
    achieved_at, created_at, updated_at
)
SELECT id, user_id, exercise_id, workout_id, weight, reps, unit,
       achieved_at, created_at, updated_at
FROM personal_records;
DROP TABLE personal_records;
ALTER TABLE personal_records_new RENAME TO personal_records;
CREATE INDEX idx_pr_user_achieved
    ON personal_records(user_id, achieved_at DESC);

CREATE TABLE personal_record_events_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    activity_id TEXT NOT NULL REFERENCES activities_new(id) ON DELETE CASCADE,
    weight REAL NOT NULL,
    reps INTEGER NOT NULL,
    unit TEXT NOT NULL CHECK(unit IN ('lb', 'kg')),
    previous_weight REAL,
    previous_reps INTEGER,
    previous_unit TEXT,
    achieved_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL
);
INSERT INTO personal_record_events_new (
    id, user_id, exercise_id, activity_id, weight, reps, unit,
    previous_weight, previous_reps, previous_unit, achieved_at, created_at
)
SELECT id, user_id, exercise_id, workout_id, weight, reps, unit,
       previous_weight, previous_reps, previous_unit, achieved_at, created_at
FROM personal_record_events;
DROP TABLE personal_record_events;
ALTER TABLE personal_record_events_new RENAME TO personal_record_events;
CREATE INDEX idx_pre_activity
    ON personal_record_events(activity_id);
CREATE INDEX idx_pre_user_achieved
    ON personal_record_events(user_id, achieved_at DESC);

-- 9. agent_memories.source_workout_id FKs into workouts (migration 035);
--    rebuild it against the new base so the workouts DROP can't cascade its
--    rows away. Column name and CHECK stay; ids preserved so the
--    vec_agent_memories join (memory_id) stays valid.
CREATE TABLE agent_memories_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           TEXT NOT NULL,
    distilled_text    TEXT NOT NULL,
    source_type       TEXT NOT NULL,
    source_session_id TEXT REFERENCES chat_sessions(id) ON DELETE CASCADE,
    source_message_id INTEGER REFERENCES chat_messages(id) ON DELETE SET NULL,
    source_workout_id TEXT REFERENCES activities_new(id) ON DELETE CASCADE,
    embedding_model   TEXT NOT NULL,
    embedding_dim     INTEGER NOT NULL,
    superseded_at     DATETIME,
    created_at        DATETIME NOT NULL,
    CHECK (
        (source_type = 'chat_session' AND source_session_id IS NOT NULL AND source_workout_id IS NULL) OR
        (source_type = 'workout_note' AND source_workout_id IS NOT NULL AND source_session_id IS NULL)
    )
);
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

-- 10. Normalize the planned-workout discriminator (column + CHECK kept; the
--     full drop happens in the stage-5 cleanup work).
UPDATE planned_workouts SET completed_session_kind = 'activity'
WHERE completed_session_kind = 'workout';

-- 11. Swap: drop the old children first (nothing references them anymore),
--     then the old parents, then rename everything into place. The RENAME of
--     activities_new rewrites every child FK target to `activities`.
--     timeline_post is untouched.
DROP TABLE activity_trackpoints;
DROP TABLE activity_best_efforts;
DROP TABLE workouts;
DROP TABLE activities;
ALTER TABLE activities_new RENAME TO activities;
ALTER TABLE activity_trackpoints_new RENAME TO activity_trackpoints;
ALTER TABLE activity_best_efforts_new RENAME TO activity_best_efforts;

CREATE UNIQUE INDEX idx_activities_dedup ON activities(user_id, ingest_source, source_activity_id)
    WHERE deleted_at IS NULL AND source_activity_id IS NOT NULL;
CREATE INDEX idx_activities_user_start ON activities(user_id, start_time DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_activities_user_type_start ON activities(user_id, activity_type, start_time DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_activity_best_efforts_distance
    ON activity_best_efforts(distance_key, duration_seconds);
