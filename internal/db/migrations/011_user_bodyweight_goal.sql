-- migrations/011_user_bodyweight_goal.sql
-- Per-user bodyweight goal. See
-- prog-strength-docs/sows/bodyweight-goal-and-page-polish.md.
--
-- One row per user (user_id is the primary key). The goal is a single
-- target weight + unit: PUT /me/bodyweight-goal takes both and the read
-- path always returns both. No goal history, no time-bounded phases.
--
-- No FK on user_id, matching the existing per-user-content tables
-- (workouts, chat_sessions, user_macro_goals): OAuth-only users aren't
-- always written to the users table before their first per-user write.

CREATE TABLE IF NOT EXISTS user_bodyweight_goal (
    user_id    TEXT     PRIMARY KEY,
    -- Target weight + denormalized unit. CHECKs match the handler-side
    -- validation; both enforce the bounds so a misbehaving client can't
    -- bypass via direct SQL once admin tooling exists.
    weight     REAL     NOT NULL CHECK (weight > 0 AND weight <= 2000),
    unit       TEXT     NOT NULL CHECK (unit IN ('lb', 'kg')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
