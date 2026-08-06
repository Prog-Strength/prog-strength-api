-- migrations/052_whoop_last_window_sync.sql
-- Durable "when did a webhook-driven window sync last succeed?" timestamp.
--
-- Why the DB and not a Prometheus counter: ingestion liveness used to be
-- alerted on increase(api_whoop_syncs_total{kind="window",result="ok"}[36h]).
-- That counter lives in process memory and resets on every API restart, and
-- WHOOP nudges us roughly once a day — so each container typically recorded
-- exactly ONE window sync and the counter sat at 1 for its whole life.
-- increase() measures last-minus-first, and 1 → (restart) → 1 has no rise and
-- no decrease for reset-detection to catch, so it evaluated to 0 and the
-- dead-ingestion alert fired permanently while ingestion was perfectly
-- healthy. A counter cannot answer "when did this last happen" across
-- restarts; a persisted timestamp can.
--
-- Nullable with no default: NULL means "no window sync recorded". In practice
-- it should never be observed — the backfill below seeds existing rows and
-- whoopconn.Upsert seeds new/reconnected ones — but the column stays nullable
-- so the seed is an explicit write rather than a silent DEFAULT that would
-- make a genuinely-never-synced row indistinguishable from a fresh one.
ALTER TABLE user_whoop_connection ADD COLUMN last_window_sync_at DATETIME;

-- Seed existing rows from updated_at rather than leaving them NULL. Connecting
-- runs a 30-day backfill, so a connection is "fresh" as of its last write; the
-- clock for "the webhook path has gone quiet" should start there, not at the
-- epoch. Without this, the freshness alert would fire on the first evaluation
-- after deploy for every pre-existing connection.
UPDATE user_whoop_connection SET last_window_sync_at = updated_at;
