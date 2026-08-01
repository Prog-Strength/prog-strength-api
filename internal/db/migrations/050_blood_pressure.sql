-- migrations/050_blood_pressure.sql
-- Blood-pressure readings: one row per measurement, systolic/diastolic
-- required plus an optional pulse. Shaped like bodyweight_entries — no FK on
-- user_id (matches the other per-user content tables), soft delete via
-- deleted_at. CHECK bounds are physiological-plausibility guards against
-- fat-fingered input, not clinical limits. See sows/blood-pressure.md.
--
-- No goal table: the healthy range is a published clinical classification,
-- identical for every user (see internal/bloodpressure/category.go), not
-- per-user state.

CREATE TABLE IF NOT EXISTS user_blood_pressure (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    systolic INTEGER NOT NULL CHECK (systolic BETWEEN 50 AND 300),
    diastolic INTEGER NOT NULL CHECK (diastolic BETWEEN 30 AND 200),
    pulse INTEGER CHECK (pulse IS NULL OR pulse BETWEEN 20 AND 250),
    measured_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    deleted_at DATETIME,
    CHECK (systolic > diastolic)
);

CREATE INDEX idx_bp_user_measured
    ON user_blood_pressure(user_id, measured_at DESC) WHERE deleted_at IS NULL;
