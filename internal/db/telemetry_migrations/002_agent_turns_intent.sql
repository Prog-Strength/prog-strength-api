-- telemetry_migrations/002_agent_turns_intent.sql
-- Adds the per-turn intent classification (and prefetch perf flags)
-- to agent_turns. Mirrors the SOW's TurnInstrumentation additions on
-- the Python side; the telemetry handler now accepts these on the
-- POST /internal/telemetry/turns body.
--
-- intent is the closed-enum string from the router
-- (log_nutrition | log_workout | log_bodyweight | analyze_progress |
-- general). Empty string means the router didn't run or completely
-- failed — kept distinct from "general" which is a deliberate
-- classifier output.

ALTER TABLE agent_turns ADD COLUMN intent TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_turns ADD COLUMN intent_prefetch_duration_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_turns ADD COLUMN intent_prefetch_failed INTEGER NOT NULL DEFAULT 0;
