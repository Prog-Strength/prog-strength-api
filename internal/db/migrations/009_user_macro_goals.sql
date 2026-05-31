-- migrations/009_user_macro_goals.sql
-- Per-user daily macro targets. See
-- prog-strength-docs/sows/daily-macro-goals.md.
--
-- One row per user (user_id is the primary key). The four targets are
-- conceptually one goal: PUT /me/macro-goals takes all four and the
-- read path always returns all four. No goal history, no time-bounded
-- phases — those are SOW non-goals.
--
-- No FK on user_id, matching the existing per-user-content tables
-- (workouts, chat_sessions): OAuth-only users aren't always written
-- to the users table before their first per-user write.

CREATE TABLE IF NOT EXISTS user_macro_goals (
    user_id    TEXT    PRIMARY KEY,
    -- Daily targets. CHECKs match the handler-side validation; both
    -- enforce the cap so a misbehaving client can't bypass via direct
    -- SQL once admin tooling exists.
    protein_g  INTEGER NOT NULL CHECK (protein_g  >= 0 AND protein_g  <= 10000),
    carbs_g    INTEGER NOT NULL CHECK (carbs_g    >= 0 AND carbs_g    <= 10000),
    fat_g      INTEGER NOT NULL CHECK (fat_g      >= 0 AND fat_g      <= 10000),
    calories   INTEGER NOT NULL CHECK (calories   >= 0 AND calories   <= 100000),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
