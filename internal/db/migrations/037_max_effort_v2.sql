-- migrations/037_max_effort_v2.sql
-- Running max-effort estimator v2: profile demographics for the engine
-- prior and window bounds on best-effort rows for pace/HR quality signals.

ALTER TABLE users ADD COLUMN birthdate TEXT;
ALTER TABLE users ADD COLUMN sex TEXT CHECK(sex IS NULL OR sex IN ('male', 'female'));

ALTER TABLE activity_best_efforts ADD COLUMN window_start_elapsed_seconds REAL;
ALTER TABLE activity_best_efforts ADD COLUMN window_end_elapsed_seconds REAL;
