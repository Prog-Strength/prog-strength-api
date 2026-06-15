-- planned_workouts: forward-looking training entity with lifecycle + Google bookkeeping.
CREATE TABLE IF NOT EXISTS planned_workouts (
    id                     TEXT PRIMARY KEY,
    user_id                TEXT NOT NULL,
    name                   TEXT,
    activity_kind          TEXT NOT NULL DEFAULT 'lift' CHECK (activity_kind IN ('lift')),
    scheduled_start_utc    DATETIME NOT NULL,
    scheduled_end_utc      DATETIME NOT NULL,
    timezone               TEXT NOT NULL,
    status                 TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned','completed','skipped')),
    notes                  TEXT,
    completed_session_id   TEXT,
    completed_session_kind TEXT CHECK (completed_session_kind IN ('workout','activity')),
    calendar_detail        TEXT CHECK (calendar_detail IN ('time_block','full_agenda')),
    google_event_id        TEXT,
    google_sync_status     TEXT CHECK (google_sync_status IN ('pending','synced','failed')),
    last_sync_error        TEXT,
    created_at             DATETIME NOT NULL,
    updated_at             DATETIME NOT NULL,
    deleted_at             DATETIME
);
CREATE INDEX IF NOT EXISTS idx_planned_workouts_user_start
    ON planned_workouts (user_id, scheduled_start_utc);

CREATE TABLE IF NOT EXISTS planned_workout_exercises (
    id                 TEXT PRIMARY KEY,
    planned_workout_id TEXT NOT NULL REFERENCES planned_workouts(id) ON DELETE CASCADE,
    exercise_id        TEXT NOT NULL,
    order_index        INTEGER NOT NULL,
    notes              TEXT
);
CREATE INDEX IF NOT EXISTS idx_planned_workout_exercises_pw
    ON planned_workout_exercises (planned_workout_id, order_index);

CREATE TABLE IF NOT EXISTS planned_sets (
    id                          TEXT PRIMARY KEY,
    planned_workout_exercise_id TEXT NOT NULL REFERENCES planned_workout_exercises(id) ON DELETE CASCADE,
    order_index                 INTEGER NOT NULL,
    target_reps                 INTEGER,
    target_weight               REAL,
    unit                        TEXT,
    target_rpe                  REAL
);
CREATE INDEX IF NOT EXISTS idx_planned_sets_pwe
    ON planned_sets (planned_workout_exercise_id, order_index);

-- users: canonical IANA timezone (for server-side Google writes) + calendar detail default.
ALTER TABLE users ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE users ADD COLUMN calendar_default_detail TEXT NOT NULL DEFAULT 'time_block';
