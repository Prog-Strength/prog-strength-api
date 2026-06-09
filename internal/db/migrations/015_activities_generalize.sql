-- migrations/015_activities_generalize.sql
-- Generalize the running domain into a sport-agnostic activity domain so a
-- future Garmin Connect / Strava / etc. ingest source can drop into the
-- same pipeline as the existing manual TCX import.
--
-- Renames:
--   running_sessions      -> activities
--   running_trackpoints   -> activity_trackpoints (session_id -> activity_id)
--   garmin_activity_id    -> source_activity_id
--
-- New columns on activities:
--   activity_type   running | walking | cycling | other
--   ingest_source   manual_tcx | garmin_api
--
-- Existing rows are all manual TCX imports of running activities, so they
-- backfill with activity_type='running' and ingest_source='manual_tcx'.
-- Dedup moves from (user_id, garmin_activity_id) to
-- (user_id, ingest_source, source_activity_id): an activity_id from Garmin's
-- TCX file and one from a future Garmin Connect API call shouldn't collide
-- just because they happen to share the same numeric value.
--
-- Existing tcx_s3_key values are left untouched. They point at the flat
-- "runs/<user_id>/<id>.tcx" scheme used before this migration; new uploads
-- use the Hive-partitioned scheme built by buildTCXKey in the activity
-- package. The repository reads the key as-is from the column, so old and
-- new objects coexist.
--
-- avg_pace_sec_per_km loses its NOT NULL constraint here: it's a running-
-- specific summary, undefined for walks or cycling. Existing rows are all
-- runs and keep their value.
--
-- SQLite has no DROP CONSTRAINT and no inline column rename that updates
-- FK references, so we use the standard create-new + INSERT…SELECT + DROP
-- + RENAME sequence (same pattern as migrations 012 and 014). FK integrity
-- is verified at COMMIT time via PRAGMA defer_foreign_keys, by which point
-- the old trackpoints table has been replaced by the new one pointing at
-- the new parent.

PRAGMA defer_foreign_keys = 1;

CREATE TABLE activities (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    activity_type TEXT NOT NULL CHECK(activity_type IN ('running', 'walking', 'cycling', 'other')),
    ingest_source TEXT NOT NULL CHECK(ingest_source IN ('manual_tcx', 'garmin_api')),
    source_activity_id TEXT NOT NULL,
    start_time DATETIME NOT NULL,
    name TEXT,
    distance_meters REAL NOT NULL,
    duration_seconds INTEGER NOT NULL,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    avg_heart_rate_bpm INTEGER,
    max_heart_rate_bpm INTEGER,
    total_calories INTEGER,
    elevation_gain_meters REAL,
    tcx_s3_key TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    deleted_at DATETIME
);

INSERT INTO activities (
    id, user_id, activity_type, ingest_source, source_activity_id,
    start_time, name, distance_meters, duration_seconds,
    avg_pace_sec_per_km, best_pace_sec_per_km,
    avg_heart_rate_bpm, max_heart_rate_bpm,
    total_calories, elevation_gain_meters,
    tcx_s3_key, created_at, deleted_at
)
SELECT
    id, user_id, 'running', 'manual_tcx', garmin_activity_id,
    start_time, name, distance_meters, duration_seconds,
    avg_pace_sec_per_km, best_pace_sec_per_km,
    avg_heart_rate_bpm, max_heart_rate_bpm,
    total_calories, elevation_gain_meters,
    tcx_s3_key, created_at, deleted_at
FROM running_sessions;

CREATE TABLE activity_trackpoints (
    activity_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    elapsed_seconds INTEGER NOT NULL,
    distance_meters REAL NOT NULL,
    heart_rate_bpm INTEGER,
    pace_sec_per_km REAL,
    elevation_meters REAL,
    PRIMARY KEY (activity_id, sequence),
    FOREIGN KEY (activity_id) REFERENCES activities(id) ON DELETE CASCADE
);

INSERT INTO activity_trackpoints (
    activity_id, sequence, elapsed_seconds, distance_meters,
    heart_rate_bpm, pace_sec_per_km, elevation_meters
)
SELECT
    session_id, sequence, elapsed_seconds, distance_meters,
    heart_rate_bpm, pace_sec_per_km, elevation_meters
FROM running_trackpoints;

DROP TABLE running_trackpoints;
DROP TABLE running_sessions;

-- Dedup: per source, live rows only. Re-uploading the same TCX file
-- collapses to one row + 409; a future Garmin Connect sync of the same
-- activity is a separate source and doesn't collide with the TCX row.
CREATE UNIQUE INDEX idx_activities_dedup
    ON activities(user_id, ingest_source, source_activity_id) WHERE deleted_at IS NULL;

-- Newest-first list query for live rows, used by the cursor and range
-- paths in handler.go (replaces idx_running_sessions_user_start).
CREATE INDEX idx_activities_user_start
    ON activities(user_id, start_time DESC) WHERE deleted_at IS NULL;

-- Activity-type-scoped list, used by the running-metrics endpoint which
-- aggregates only over activity_type='running'.
CREATE INDEX idx_activities_user_type_start
    ON activities(user_id, activity_type, start_time DESC) WHERE deleted_at IS NULL;
