-- migrations/033_workout_tcx_association.sql
-- Let a strength workout carry the heart-rate / effort layer of a Garmin
-- "Strength Training" TCX. The TCX lands as a row in the existing
-- activities domain (a new activity_type), and the workout points at it
-- through a single nullable link column. See
-- prog-strength-docs/sows/workout-tcx-enrichment.md.
--
-- Two changes:
--
--   1. Widen the activities.activity_type CHECK to admit
--      'strength_training'. SQLite can't ALTER a CHECK in place, so this is
--      a create-new + INSERT…SELECT + DROP + RENAME table rebuild, the same
--      shape migration 015 used to generalize running_sessions.
--
--      The wrinkle 015 didn't have: here the rebuilt parent keeps its name
--      ("activities"), so the old table must be dropped. Our migrations run
--      inside a transaction on a foreign_keys=ON connection, where
--      `PRAGMA foreign_keys=OFF` is a no-op and `defer_foreign_keys` defers
--      constraint *checking* but NOT the ON DELETE CASCADE *action*. So
--      dropping the old `activities` would fire the implicit row delete and
--      cascade away every activity_trackpoints / activity_best_efforts row.
--      To avoid that we rebuild the two child tables too, pointing their FKs
--      at `activities_new`; dropping the old `activities` then cascades into
--      nothing (the old children are already gone), and the RENAME of
--      activities_new -> activities rewrites the child FK references to the
--      final name (SQLite updates FK references on RENAME with
--      legacy_alter_table off, the default). Net effect: the data and the
--      activity_trackpoints FK survive intact, verified by the populated-DB
--      migration test.
--
--   2. Add workouts.activity_id: a nullable soft reference to activities(id)
--      (no hard FK, matching the repo's cross-domain convention — workouts
--      already soft-reference user_id without an FK). A partial unique index
--      enforces at most one live workout per activity.

PRAGMA defer_foreign_keys = 1;

-- New parent with the widened activity_type CHECK. Column list is otherwise
-- byte-for-byte the post-015 schema.
CREATE TABLE activities_new (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    activity_type TEXT NOT NULL CHECK(activity_type IN ('running', 'walking', 'cycling', 'other', 'strength_training')),
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
INSERT INTO activities_new SELECT * FROM activities;

-- Rebuild the children referencing activities_new, copying their rows, so the
-- DROP of the old activities below cannot cascade into them.
CREATE TABLE activity_trackpoints_new (
    activity_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    elapsed_seconds INTEGER NOT NULL,
    distance_meters REAL NOT NULL,
    heart_rate_bpm INTEGER,
    pace_sec_per_km REAL,
    elevation_meters REAL,
    PRIMARY KEY (activity_id, sequence),
    FOREIGN KEY (activity_id) REFERENCES activities_new(id) ON DELETE CASCADE
);
INSERT INTO activity_trackpoints_new SELECT * FROM activity_trackpoints;

CREATE TABLE activity_best_efforts_new (
    activity_id TEXT NOT NULL,
    distance_key TEXT NOT NULL,
    duration_seconds REAL NOT NULL,
    PRIMARY KEY (activity_id, distance_key),
    FOREIGN KEY (activity_id) REFERENCES activities_new(id) ON DELETE CASCADE,
    CHECK(distance_key IN ('1mi', '2mi', '5k', '10k', 'half_marathon', 'marathon'))
);
INSERT INTO activity_best_efforts_new SELECT * FROM activity_best_efforts;

-- Drop the children first (so the parent drop has nothing to cascade into),
-- then the parent.
DROP TABLE activity_trackpoints;
DROP TABLE activity_best_efforts;
DROP TABLE activities;

-- Rename into place. Renaming activities_new -> activities rewrites the child
-- FK references from activities_new to activities.
ALTER TABLE activities_new RENAME TO activities;
ALTER TABLE activity_trackpoints_new RENAME TO activity_trackpoints;
ALTER TABLE activity_best_efforts_new RENAME TO activity_best_efforts;

-- Recreate every index that lived on the rebuilt tables (identical to 015/016).
CREATE UNIQUE INDEX idx_activities_dedup
    ON activities(user_id, ingest_source, source_activity_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_activities_user_start
    ON activities(user_id, start_time DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_activities_user_type_start
    ON activities(user_id, activity_type, start_time DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_activity_best_efforts_distance
    ON activity_best_efforts(distance_key, duration_seconds);

-- 2. Link column on workouts. Nullable; null = no TCX attached. Soft
-- reference (no FK) so the workout and activity domains stay decoupled.
ALTER TABLE workouts ADD COLUMN activity_id TEXT;

-- At most one live workout may point at a given activity. Partial on the
-- non-null, non-deleted rows so detached/soft-deleted workouts don't collide
-- and the many workouts with a NULL activity_id never conflict.
CREATE UNIQUE INDEX idx_workouts_activity
    ON workouts(activity_id) WHERE activity_id IS NOT NULL AND deleted_at IS NULL;
