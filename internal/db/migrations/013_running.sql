-- migrations/013_running.sql
-- Running tracking via Garmin TCX import. See
-- prog-strength-docs/sows/running-tracking-via-tcx-import.md.
--
-- Adds two new tables and one user column. A TCX upload is split across
-- two stores: the summary fields the run-list and stat tiles need land
-- in running_sessions; a downsampled (~300-point) trackpoint series for
-- the detail-page charts lands in running_trackpoints. The original file
-- lives in S3 and isn't modeled here.
--
-- running_sessions carries the dedup key UNIQUE(user_id, garmin_activity_id)
-- so a re-export/re-upload of the same run collapses to one row + a 409.
-- It soft-deletes via deleted_at; the partial index serves the newest-first
-- list query for live rows only. user_id is not a FK, matching the existing
-- nutrition/workouts pattern (users aren't always in the DB).
--
-- running_trackpoints hangs off a session with ON DELETE CASCADE so a hard
-- delete of a session takes its points with it. CASCADE relies on the
-- foreign_keys pragma, which the repo enables (_foreign_keys=on).
--
-- The new users.distance_unit column mirrors the existing weight_unit
-- column's shape (NOT NULL + two-value CHECK). The DEFAULT 'mi' backfills
-- existing rows per SOW Open Question #1 (lean a: backfill to miles).

CREATE TABLE IF NOT EXISTS running_sessions (
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
    deleted_at DATETIME,
    -- Dedup: re-uploading the same Garmin activity collapses to one row.
    UNIQUE(user_id, garmin_activity_id)
);

-- Partial index for the newest-first list query over live sessions only.
CREATE INDEX idx_running_sessions_user_start
    ON running_sessions(user_id, start_time DESC) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS running_trackpoints (
    session_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    elapsed_seconds INTEGER NOT NULL,
    distance_meters REAL NOT NULL,
    heart_rate_bpm INTEGER,
    pace_sec_per_km REAL,
    elevation_meters REAL,
    PRIMARY KEY (session_id, sequence),
    FOREIGN KEY (session_id) REFERENCES running_sessions(id) ON DELETE CASCADE
);

-- Distance display preference, parallel to weight_unit (migration 001).
-- DEFAULT 'mi' backfills existing rows (SOW Open Question #1, lean a).
ALTER TABLE users ADD COLUMN distance_unit TEXT NOT NULL DEFAULT 'mi' CHECK(distance_unit IN ('mi', 'km'));
