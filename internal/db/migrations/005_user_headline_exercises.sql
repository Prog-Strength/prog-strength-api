-- migrations/005_user_headline_exercises.sql
-- Per-user override of which exercises are surfaced on the Personal
-- Records page. See prog-strength-docs/sows/custom-headline-lifts.md.
--
-- Empty by default. The read path falls back to the global curated
-- list (workout.HeadlineExercises in Go) when a user has zero rows
-- here, so existing users see no behavior change until they save
-- their first custom selection.
--
-- The table is intentionally scoped to the existing `exercises`
-- catalog. If cardio (running etc.) ships later it'll get its own
-- catalog and a sibling user_headline_<discipline> table — the
-- framing word "headline" is the shared concept, the storage stays
-- per-discipline.

CREATE TABLE IF NOT EXISTS user_headline_exercises (
    user_id TEXT NOT NULL,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    -- 0-indexed display order. The write path always re-numbers
    -- positions densely from 0 inside the same transaction that
    -- replaces the user's rows, so gaps and duplicates are
    -- structurally impossible.
    position INTEGER NOT NULL CHECK(position >= 0),
    created_at DATETIME NOT NULL,
    PRIMARY KEY (user_id, exercise_id)
);

-- Slot uniqueness within a user, AND the dominant read query:
-- "give me this user's selection in display order." A unique
-- composite covers both — SQLite reuses it for the ordered read,
-- so a second plain index would just duplicate storage.
CREATE UNIQUE INDEX idx_uhe_user_position
    ON user_headline_exercises(user_id, position);
