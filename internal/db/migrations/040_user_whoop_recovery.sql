-- user_whoop_recovery: steps-shaped daily recovery metric table (cf. 019_steps),
-- one row per (user, local calendar date). Hard-deleted, upsert on conflict —
-- latest score wins. sleep_id is stored because v2 webhook delete events
-- identify a recovery by its associated sleep UUID.
CREATE TABLE IF NOT EXISTS user_whoop_recovery (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL,
    date               TEXT NOT NULL,
    recovery_score     REAL,
    resting_heart_rate REAL,
    hrv_rmssd_milli    REAL,
    cycle_id           INTEGER NOT NULL,
    sleep_id           TEXT NOT NULL,
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL,
    UNIQUE (user_id, date)
);
CREATE INDEX IF NOT EXISTS idx_user_whoop_recovery_user_date ON user_whoop_recovery(user_id, date DESC);
CREATE INDEX IF NOT EXISTS idx_user_whoop_recovery_sleep ON user_whoop_recovery(user_id, sleep_id);
