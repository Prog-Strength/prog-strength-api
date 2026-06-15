-- migrations/027_planned_workout_run_kind.sql
-- Let a planned workout be a run as well as a lift.
--
-- The planned_workouts.activity_kind column shipped with a CHECK constraint
-- that only allowed 'lift'. This widens it to allow 'run' and adds two
-- run-specific, nullable columns:
--   run_type      easy | threshold | intervals   (the kind of run)
--   run_details   free text (target pace, interval breakdown, …)
--
-- A run's "agenda" is this run_type + run_details pair, mirroring how a lift's
-- agenda is its planned_workout_exercises rows. Both are optional: either kind
-- can be a bare time block. Existing rows are all lifts and backfill both new
-- columns to NULL.
--
-- SQLite has no DROP/ALTER CONSTRAINT, so widening the activity_kind CHECK
-- needs the standard create-new + INSERT…SELECT + DROP + RENAME rebuild (same
-- pattern as migrations 012/014/015). The child tables
-- (planned_workout_exercises → planned_sets) reference planned_workouts(id);
-- PRAGMA defer_foreign_keys holds their FK check until COMMIT, by which point
-- the renamed table carries the same ids, so integrity is preserved.

PRAGMA defer_foreign_keys = 1;

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
    completed_session_kind TEXT CHECK (completed_session_kind IN ('workout','activity')),
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
    timezone, status, notes, completed_session_id, completed_session_kind,
    calendar_detail, google_event_id, google_sync_status, last_sync_error,
    run_type, run_details, created_at, updated_at, deleted_at
)
SELECT
    id, user_id, name, activity_kind, scheduled_start_utc, scheduled_end_utc,
    timezone, status, notes, completed_session_id, completed_session_kind,
    calendar_detail, google_event_id, google_sync_status, last_sync_error,
    NULL, NULL, created_at, updated_at, deleted_at
FROM planned_workouts;

DROP TABLE planned_workouts;
ALTER TABLE planned_workouts_new RENAME TO planned_workouts;

CREATE INDEX IF NOT EXISTS idx_planned_workouts_user_start
    ON planned_workouts (user_id, scheduled_start_utc);
