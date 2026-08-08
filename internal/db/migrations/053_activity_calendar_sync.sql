-- migrations/053_activity_calendar_sync.sql
-- Google Calendar sync bookkeeping for LOGGED activities, one row per synced
-- activity. The planned-workout side already carries its own sync columns
-- (google_event_id / google_sync_status / last_sync_error) directly on the
-- planned_workout row; this is the completed-session counterpart.
--
-- WHY A SIDE TABLE RATHER THAN COLUMNS ON `activities` — the unified activity
-- model's premise (042) is a lean, sport-agnostic base row that every type
-- shares. Google Calendar is one optional integration used by a minority of
-- users, so putting four of its columns on the base row would make every read
-- of every activity, for every user, carry sync state it does not use. Keeping
-- it beside the base table also means:
--   * the reconciler's "which activities were never synced" question is a
--     cheap anti-join against a small table, not a scan of a wide one;
--   * a second calendar provider gets its own table without a migration on
--     `activities`;
--   * disconnecting can drop sync state wholesale without touching sessions.
--
-- Rows are created LAZILY: an activity only gets one once a sync has been
-- attempted for it. Absence therefore means "never attempted", which is
-- exactly the signal the reconciler's NOT EXISTS looks for. Do not seed rows
-- at activity-create time — that would erase the distinction between "not yet
-- synced" and "synced and then the event was deleted".
CREATE TABLE IF NOT EXISTS activity_calendar_sync (
    activity_id     TEXT PRIMARY KEY REFERENCES activities(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL,

    -- The Google event id this activity owns. NULL means no live event: either
    -- the first insert has not succeeded yet, or the event was deleted. An
    -- adopted id (taken over from the planned workout this activity completed)
    -- is stored here verbatim — the plan and the activity deliberately
    -- reference the SAME event, and Google's 404/410 makes the double-delete
    -- that implies harmless.
    google_event_id TEXT,

    -- 'pending' | 'synced' | 'failed'. No CHECK constraint, matching the
    -- convention 047/051 set: SQLite cannot widen a table-level CHECK in
    -- place, and Go owns the closed set.
    sync_status     TEXT NOT NULL,
    last_sync_error TEXT,

    -- Attempt accounting, so a permanently-poisoned activity (a render that
    -- Google rejects, say) cannot be retried by the reconciler on every boot
    -- forever. Reset to 0 on success. Same rationale as activity_photo.attempts
    -- in 051.
    attempts        INTEGER NOT NULL DEFAULT 0,
    updated_at      DATETIME NOT NULL
);

-- Serves the reconciler's per-user sweep for rows still worth retrying
-- (sync_status = 'failed') and the freshness exporter's scan. user_id leads
-- because every read is user-scoped.
CREATE INDEX IF NOT EXISTS idx_activity_calendar_sync_user
    ON activity_calendar_sync(user_id, sync_status);

-- last_successful_sync_at on the connection is the DURABLE liveness signal the
-- alert reads, and it exists because of a bug this repo already paid for once.
-- The WHOOP dead-ingestion alert originally evaluated
-- increase(api_whoop_syncs_total{...}[36h]) and fired continuously over
-- perfectly healthy ingestion: a per-process counter resets on every restart,
-- and 1 -> (restart) -> 1 is neither a rise nor a decrease, so increase() read
-- 0 no matter how well things worked (see migration 052 and rules-whoop.yml).
--
-- A counter structurally cannot answer "when did this last succeed" across
-- restarts. Durable state can. The exporter publishes the newest stamp across
-- connected users, and the alert asks time() - <that> > threshold.
ALTER TABLE user_calendar_connection ADD COLUMN last_successful_sync_at DATETIME;
