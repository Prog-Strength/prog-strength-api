-- migrations/054_dashboard_quote_reroll.sql
-- Where a user's rerolled daily quote lives, so tapping "New quote" survives a
-- refresh (and follows the user to another browser) instead of snapping back to
-- the day's quote on the next page load.
--
-- ONE ROW PER USER, NOT ONE PER DAY. The obvious shape is
-- PRIMARY KEY (user_id, local_date), keeping the reroll history. Nothing reads
-- history: the only question ever asked is "what is this user's offset TODAY",
-- so per-day rows would accumulate forever for data no query touches, and would
-- eventually need a reaper. Keyed by user_id alone the row is simply
-- overwritten on the next reroll, which makes the table self-limiting at one
-- row per user with no cleanup job.
--
-- local_date is what makes that safe. It records the user's local calendar date
-- at the moment of the reroll, and the read path compares it against the user's
-- local date NOW: a row from yesterday loses and the reader falls back to
-- offset 0. That is how the reroll expires at the user's own local midnight —
-- the same boundary the daily quote itself turns over on — without anything
-- having to delete it.
--
-- quote_offset, not `offset`: OFFSET is a SQL keyword.
--
-- The offset is stored rather than the quote id. Offsets are what
-- quotes.PickAt walks, so the next tap after a reload is genuinely the *next*
-- quote. The trade is that a stored offset points at a different quote if the
-- corpus grows — accepted deliberately: the tile is decorative and the row
-- expires at local midnight anyway.
--
-- ON DELETE CASCADE drops the row when the user is hard-deleted, matching 049.
CREATE TABLE user_dashboard_quote_rerolls (
    user_id      TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    local_date   TEXT NOT NULL,    -- YYYY-MM-DD in the user's zone at reroll time
    quote_offset INTEGER NOT NULL,
    updated_at   TIMESTAMP NOT NULL
);
