-- migrations/031_planned_exercise_superset.sql
-- planned_workout_exercises.superset_group — nullable group identifier
-- mirroring workout_exercises.superset_group (migration 002). Planned
-- exercises sharing the same non-null value are intended to be performed as
-- a superset (alternating sets). Nullable for standalone exercises; the
-- frontend picks the convention (typically 1, 2, 3 within a single plan).

ALTER TABLE planned_workout_exercises
  ADD COLUMN superset_group INTEGER;
