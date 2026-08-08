-- migrations/055_dashboard_layout_sections.sql
-- Dashboard sections: the per-user layout stops being a flat ordered array of
-- tile ids and becomes an ordered array of SECTIONS, each owning its tiles.
-- See sows/dashboard-sections.md.
--
-- Shape of the new `sections` blob:
--
--   [{"id":"s1","title":"Recovery","collapsed":false,"tile_ids":["hrv_balance"]}]
--
-- Every existing row is wrapped into a SINGLE UNTITLED section holding its
-- tile_ids in order. An untitled section renders as a bare grid — no header, no
-- rule — so a user who never touches this feature sees the dashboard they
-- already had. The wrap is lossless and deterministic: unwrapping the first
-- section's tile_ids fully reconstructs the dropped column.
--
-- As with 049, there is NO CHECK constraint on the blob. The Go catalog
-- (internal/dashboard/tiles.go) remains the source of truth for the closed tile
-- set: the write path validates and the read path filters, so retiring a tile
-- still needs no migration. The same holds for the section invariants (global
-- tile uniqueness, id uniqueness, the 12-section and 40-char caps) — they are
-- enforced in Go on write and re-enforced on read, not in SQL.

ALTER TABLE user_dashboard_layouts ADD COLUMN sections TEXT NOT NULL DEFAULT '[]';

-- json(tile_ids) embeds the stored array as JSON rather than as a quoted
-- string; json('false') does the same for the boolean.
UPDATE user_dashboard_layouts
SET sections = json_array(
        json_object(
            'id',        's1',
            'title',     '',
            'collapsed', json('false'),
            'tile_ids',  json(tile_ids)
        )
    );

ALTER TABLE user_dashboard_layouts DROP COLUMN tile_ids;
