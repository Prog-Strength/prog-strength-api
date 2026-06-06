-- migrations/014_running_dedup_live_only.sql
-- Fix: the running-session dedup constraint should only apply to LIVE
-- rows. A user who soft-deletes a run and then re-imports the same TCX
-- (e.g. to pick up an algorithm change in the summarizer, like the new
-- pace outlier filter) was blocked by `UNIQUE(user_id, garmin_activity_id)`
-- because the unconditional table-level UNIQUE counts soft-deleted rows.
--
-- SQLite supports partial UNIQUE indexes but not partial UNIQUE table
-- constraints, so the fix is to drop the table-level UNIQUE and replace
-- it with a partial UNIQUE INDEX scoped to live rows. SQLite has no
-- DROP CONSTRAINT, so the standard create → INSERT…SELECT → DROP →
-- RENAME sequence applies (same pattern as migration 012).
--
-- running_trackpoints holds a FOREIGN KEY into running_sessions(id), and
-- the migration runner opens the connection with _foreign_keys=on, so
-- we use `PRAGMA defer_foreign_keys=1` inside the transaction. FK
-- integrity is verified at COMMIT time, by which point the rename has
-- restored the parent table — trackpoints rows continue to resolve.

PRAGMA defer_foreign_keys = 1;

CREATE TABLE running_sessions_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    garmin_activity_id TEXT NOT NULL,
    start_time DATETIME NOT NULL,
    name TEXT,
    distance_meters REAL NOT NULL,
    duration_seconds INTEGER NOT NULL,
    avg_pace_sec_per_km REAL NOT NULL,
    best_pace_sec_per_km REAL,
    avg_heart_rate_bpm INTEGER,
    max_heart_rate_bpm INTEGER,
    total_calories INTEGER,
    elevation_gain_meters REAL,
    tcx_s3_key TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    deleted_at DATETIME
    -- No table-level UNIQUE — dedup now lives in the partial index below.
);

INSERT INTO running_sessions_new (
    id, user_id, garmin_activity_id, start_time, name,
    distance_meters, duration_seconds, avg_pace_sec_per_km,
    best_pace_sec_per_km, avg_heart_rate_bpm, max_heart_rate_bpm,
    total_calories, elevation_gain_meters, tcx_s3_key,
    created_at, deleted_at
)
SELECT
    id, user_id, garmin_activity_id, start_time, name,
    distance_meters, duration_seconds, avg_pace_sec_per_km,
    best_pace_sec_per_km, avg_heart_rate_bpm, max_heart_rate_bpm,
    total_calories, elevation_gain_meters, tcx_s3_key,
    created_at, deleted_at
FROM running_sessions;

DROP TABLE running_sessions;

ALTER TABLE running_sessions_new RENAME TO running_sessions;

-- Partial UNIQUE — dedup applies only to live rows. A soft-deleted row
-- no longer blocks re-import of the same Garmin activity.
CREATE UNIQUE INDEX idx_running_sessions_dedup
    ON running_sessions(user_id, garmin_activity_id) WHERE deleted_at IS NULL;

-- Recreate the partial index for the newest-first list query (originally
-- created in migration 013 and dropped along with the table above).
CREATE INDEX idx_running_sessions_user_start
    ON running_sessions(user_id, start_time DESC) WHERE deleted_at IS NULL;
