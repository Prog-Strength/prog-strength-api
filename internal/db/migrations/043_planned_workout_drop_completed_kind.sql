-- migrations/043_planned_workout_drop_completed_kind.sql
-- Collapse the planned_workouts.completed_session_kind discriminator.
--
-- Stage 5 of the unified-activity-model SOW. Every training session — lift or
-- run — now lives in the one `activities` base table with a globally-unique id,
-- so completed_session_id alone identifies the completing session and its
-- activity_type derives the plan kind it can complete. The kind discriminator
-- is dead weight; migration 042 already normalized its live values to
-- 'activity' for pre-existing links. This drops the column outright.
--
-- SQLite has no DROP COLUMN that also drops the column's CHECK, so this is the
-- standard create-new + INSERT…SELECT + DROP + RENAME rebuild (pattern of
-- migrations 015/027/042). All other columns and the user_start index are
-- reproduced exactly.
--
-- The agenda children (planned_workout_exercises → planned_sets) are rebuilt
-- alongside the parent even though their schema is unchanged: DROP TABLE fires
-- ON DELETE CASCADE even under defer_foreign_keys (the lesson documented in
-- migrations 033/042), so dropping planned_workouts with live child FKs would
-- silently cascade the whole agenda away. Rebuilding each child to reference
-- the *_new parent BEFORE the old parents are dropped, then renaming bottom-up,
-- keeps every agenda row intact and re-points each FK target name back to the
-- canonical table on RENAME.

PRAGMA defer_foreign_keys = 1;

-- 1. Rebuild the parent without completed_session_kind.
CREATE TABLE planned_workouts_new (
    id                     TEXT PRIMARY KEY,
    user_id                TEXT NOT NULL,
    name                   TEXT,
    activity_kind          TEXT NOT NULL DEFAULT 'lift' CHECK (activity_kind IN ('lift','run')),
    scheduled_start_utc    DATETIME NOT NULL,
    scheduled_end_utc      DATETIME NOT NULL,
    timezone               TEXT NOT NULL,
    status                 TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned','completed','skipped')),
    notes                  TEXT,
    completed_session_id   TEXT,
    calendar_detail        TEXT CHECK (calendar_detail IN ('time_block','full_agenda')),
    google_event_id        TEXT,
    google_sync_status     TEXT CHECK (google_sync_status IN ('pending','synced','failed')),
    last_sync_error        TEXT,
    run_type               TEXT CHECK (run_type IN ('easy','threshold','intervals')),
    run_details            TEXT,
    created_at             DATETIME NOT NULL,
    updated_at             DATETIME NOT NULL,
    deleted_at             DATETIME
);

INSERT INTO planned_workouts_new (
    id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
    timezone, status, notes, completed_session_id,
    calendar_detail, google_event_id, google_sync_status, last_sync_error,
    run_type, run_details, created_at, updated_at, deleted_at
)
SELECT
    id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
    timezone, status, notes, completed_session_id,
    calendar_detail, google_event_id, google_sync_status, last_sync_error,
    run_type, run_details, created_at, updated_at, deleted_at
FROM planned_workouts;

-- 2. Rebuild the agenda children so their FKs point at planned_workouts_new
--    before the old parent is dropped. Bottom of the chain first is not
--    required for the copy, but every _new child must exist and reference a
--    _new parent before any old table is dropped.
CREATE TABLE planned_workout_exercises_new (
    id                 TEXT PRIMARY KEY,
    planned_workout_id TEXT NOT NULL REFERENCES planned_workouts_new(id) ON DELETE CASCADE,
    exercise_id        TEXT NOT NULL,
    order_index        INTEGER NOT NULL,
    notes              TEXT,
    superset_group     INTEGER
);

INSERT INTO planned_workout_exercises_new (
    id, planned_workout_id, exercise_id, order_index, notes, superset_group
)
SELECT id, planned_workout_id, exercise_id, order_index, notes, superset_group
FROM planned_workout_exercises;

CREATE TABLE planned_sets_new (
    id                          TEXT PRIMARY KEY,
    planned_workout_exercise_id TEXT NOT NULL REFERENCES planned_workout_exercises_new(id) ON DELETE CASCADE,
    order_index                 INTEGER NOT NULL,
    target_reps                 INTEGER,
    target_weight               REAL,
    unit                        TEXT,
    target_rpe                  REAL,
    amrap                       INTEGER NOT NULL DEFAULT 0
);

INSERT INTO planned_sets_new (
    id, planned_workout_exercise_id, order_index,
    target_reps, target_weight, unit, target_rpe, amrap
)
SELECT id, planned_workout_exercise_id, order_index,
    target_reps, target_weight, unit, target_rpe, amrap
FROM planned_sets;

-- 3. Drop old tables children-first (so no drop hits a live child FK), then
--    rename every _new into place. Renaming planned_workouts_new rewrites the
--    exercises FK target; renaming exercises rewrites the sets FK target.
DROP TABLE planned_sets;
DROP TABLE planned_workout_exercises;
DROP TABLE planned_workouts;
ALTER TABLE planned_workouts_new RENAME TO planned_workouts;
ALTER TABLE planned_workout_exercises_new RENAME TO planned_workout_exercises;
ALTER TABLE planned_sets_new RENAME TO planned_sets;

-- 4. Recreate indexes (identical to migrations 025/027).
CREATE INDEX IF NOT EXISTS idx_planned_workouts_user_start
    ON planned_workouts (user_id, scheduled_start_utc);
CREATE INDEX IF NOT EXISTS idx_planned_workout_exercises_pw
    ON planned_workout_exercises (planned_workout_id, order_index);
CREATE INDEX IF NOT EXISTS idx_planned_sets_pwe
    ON planned_sets (planned_workout_exercise_id, order_index);
