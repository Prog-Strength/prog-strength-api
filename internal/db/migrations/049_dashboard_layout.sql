-- migrations/049_dashboard_layout.sql
-- Per-user dashboard layout: one ordered JSON array of enabled tile ids per
-- user. No row means "never customized" -> the read path resolves the default
-- layout, reproducing today's dashboard. See sows/customizable-dashboard-tiles.md.
--
-- tile_ids carries NO CHECK constraint. Consistent with migration 042's
-- treatment of activity_type, the Go catalog (internal/dashboard/tiles.go) is
-- the source of truth for the closed set: the write path validates and the read
-- path filters unknown ids. Retiring a tile therefore never needs a migration
-- and never breaks a stored layout.
--
-- Keyed by user_id (PK); every read and write is by user_id, so no other index.
-- ON DELETE CASCADE drops a user's layout when the user is hard-deleted.

CREATE TABLE user_dashboard_layouts (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    tile_ids   TEXT NOT NULL,   -- JSON array of tile ids, in display order
    updated_at TIMESTAMP NOT NULL
);
