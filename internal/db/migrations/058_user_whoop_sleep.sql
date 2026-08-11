-- migrations/058_user_whoop_sleep.sql
-- WHOOP sleep records. See sows/whoop-sleep-ingestion.md.
--
-- Two deliberate departures from 040_user_whoop_recovery.sql (and from the
-- steps-shaped daily-metric pattern generally):
--
-- 1. ONE ROW PER WHOOP SLEEP RECORD, not per (user, date). WHOOP emits a
--    separate record with nap=true for daytime sleep, so (user_id, date) is
--    genuinely not unique and a daily upsert would silently drop whichever of
--    a nap and a night arrived second. Uniqueness is (user_id, whoop_sleep_id).
--    Keying on WHOOP's own UUID also makes a sleep.deleted webhook a direct
--    delete rather than a date-derivation round trip.
--
-- 2. DURATIONS IN MILLISECONDS EXACTLY AS WHOOP SENDS THEM. Storing the wire
--    value keeps ingest dumb and reversible — the original response is always
--    recoverable from the row — and presentation rounding stays the tile's job.
--
-- date is the local WAKE date: the record's `end` localized by its own
-- timezone_offset. Dating by `start` is the bug migration 041 exists to
-- commemorate, and it would also land a night on a different day than the
-- recovery computed from it.
--
-- Every score column is nullable because a PENDING record has a start, an end,
-- and nothing else — and it is still stored, so the row already exists when
-- the score arrives and the webhook's re-sync is a plain upsert.
CREATE TABLE IF NOT EXISTS user_whoop_sleep (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    whoop_sleep_id      TEXT NOT NULL,      -- v2 UUID; the webhook delete key
    date                TEXT NOT NULL,      -- YYYY-MM-DD local WAKE date
    is_nap              INTEGER NOT NULL,   -- 0/1
    started_at          TEXT NOT NULL,      -- RFC3339, as WHOOP sent it
    ended_at            TEXT NOT NULL,      -- RFC3339, as WHOOP sent it
    timezone_offset     TEXT NOT NULL,      -- e.g. "-06:00", as WHOOP sent it
    score_state         TEXT NOT NULL,      -- SCORED | PENDING | UNSCORABLE

    -- stage summary (nullable: absent unless SCORED)
    in_bed_milli            INTEGER,
    awake_milli             INTEGER,
    no_data_milli           INTEGER,
    light_sleep_milli       INTEGER,
    slow_wave_sleep_milli   INTEGER,
    rem_sleep_milli         INTEGER,
    sleep_cycle_count       INTEGER,
    disturbance_count       INTEGER,

    -- sleep need (nullable)
    need_baseline_milli         INTEGER,
    need_from_sleep_debt_milli  INTEGER,
    need_from_strain_milli      INTEGER,
    need_from_nap_milli         INTEGER,

    -- scored percentages + respiratory rate (nullable REALs)
    respiratory_rate            REAL,
    performance_pct             REAL,
    consistency_pct             REAL,
    efficiency_pct              REAL,

    -- DATETIME, not the SOW's TEXT, so database/sql scans them straight into
    -- time.Time exactly as 040's do.
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_whoop_sleep_record
    ON user_whoop_sleep(user_id, whoop_sleep_id);
CREATE INDEX IF NOT EXISTS idx_user_whoop_sleep_date
    ON user_whoop_sleep(user_id, date);
