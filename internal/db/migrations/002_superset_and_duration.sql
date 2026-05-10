-- migrations/002_superset_and_duration.sql
-- Two related modeling additions to the workout schema:
--
--   * workouts.ended_at — nullable end timestamp. Duration of a session
--     is computable as ended_at - performed_at. Nullable so historical
--     entries logged before this column existed remain valid.
--
--   * workout_exercises.superset_group — nullable group identifier.
--     Workout exercises sharing the same value were performed as a
--     superset (alternating sets). Nullable for standalone exercises.
--     Any non-null integer is valid; the frontend picks the convention
--     (typically 1, 2, 3 within a single workout).

ALTER TABLE workouts
  ADD COLUMN ended_at DATETIME;

ALTER TABLE workout_exercises
  ADD COLUMN superset_group INTEGER;
