-- migrations/019_steps.sql
-- Daily step totals, one row per (user, calendar date).
--
-- Unlike bodyweight (a measurement keyed by timestamp), steps are a
-- date-keyed running total: a device or the user reports a single
-- cumulative count per day, so re-logging the same day overwrites the
-- prior total rather than appending. That upsert semantic is enforced
-- by the UNIQUE (user_id, date) constraint — ON CONFLICT replaces the
-- count and bumps updated_at while preserving created_at.
--
-- Steps are unitless (no per-row unit denormalization like bodyweight)
-- and hard-deleted (no soft-delete / audit trail): a step count is
-- disposable derived data, not history worth retaining. The composite
-- DESC index serves the newest-first range + keyset read paths.
CREATE TABLE IF NOT EXISTS user_steps (
    id         TEXT     PRIMARY KEY,
    user_id    TEXT     NOT NULL,
    date       TEXT     NOT NULL,
    steps      INTEGER  NOT NULL CHECK (steps >= 0 AND steps <= 200000),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (user_id, date)
);
CREATE INDEX IF NOT EXISTS idx_user_steps_user_date ON user_steps(user_id, date DESC);

-- Per-user daily step goal: a singleton row, set-replacement semantics,
-- mirroring user_bodyweight_goal. The "never set" state is the absence
-- of a row, which the read path collapses to a zero-valued goal.
CREATE TABLE IF NOT EXISTS user_steps_goal (
    user_id    TEXT     PRIMARY KEY,
    goal       INTEGER  NOT NULL CHECK (goal > 0 AND goal <= 200000),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
