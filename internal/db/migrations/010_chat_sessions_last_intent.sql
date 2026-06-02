-- migrations/010_chat_sessions_last_intent.sql
-- Adds the "last intent classified for this conversation" pair of
-- columns used by the intent-driven-context-enrichment SOW. The
-- agent reads these as a hint to its router and writes the new
-- value back via the existing telemetry POST.
--
-- Both columns nullable. NULL means "no prior intent known," which
-- the router falls back to gracefully by classifying on conversation
-- context alone.

ALTER TABLE chat_sessions ADD COLUMN last_intent TEXT;
ALTER TABLE chat_sessions ADD COLUMN last_intent_at DATETIME;
