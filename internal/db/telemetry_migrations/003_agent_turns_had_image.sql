-- telemetry_migrations/003_agent_turns_had_image.sql
-- Adds the per-turn had_image flag to agent_turns. Mirrors the SOW's
-- TurnInstrumentation addition on the Python side; the telemetry
-- handler now accepts had_image on the POST /internal/telemetry/turns
-- body so photo-meal-logging usage is measurable.
--
-- had_image is a boolean stored as INTEGER (0/1). Older clients that
-- omit it default to false (0) via the column default AND the Go zero
-- value, so pre-feature agents keep writing without a 400.

ALTER TABLE agent_turns ADD COLUMN had_image INTEGER NOT NULL DEFAULT 0;
