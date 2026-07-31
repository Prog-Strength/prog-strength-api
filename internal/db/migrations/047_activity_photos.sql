-- migrations/047_activity_photos.sql
-- Activity Photos: attach uploaded photos (full + thumbnail S3 variants) to any
-- activity. See prog-strength-docs/sows/activity-photos.md.
--
-- Additive-only: one CREATE TABLE plus two indexes, no rebuild-and-copy. Nothing
-- references this table yet, so there is no parent to re-point and no backfill —
-- the same expand-only shape as 038_activity_route.sql / 036_activity_environment.sql.
--
-- content_type carries NO CHECK constraint. SQLite cannot widen a table-level
-- CHECK in place (the rebuild lesson from 042/045), and the accepted image types
-- will grow over time; Go owns the allowlist so widening never costs a migration.
--
-- position is 0-based but deliberately NOT unique: reorder rewrites the whole set
-- in one transaction, and enforcing uniqueness would make the intermediate states
-- of that rewrite illegal. Ties (should any transiently occur) break on id, which
-- is why id is the trailing column of the ordered read index.
--
-- Soft delete on both sides: deleted_at NULL = live. A photo can be removed
-- without a hard DELETE, and the activities(id) FK still CASCADEs a hard parent
-- delete so no orphan rows survive.

CREATE TABLE activity_photo (
    id           TEXT PRIMARY KEY,
    activity_id  TEXT NOT NULL REFERENCES activities(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL,                          -- denormalized owner for per-user aggregates
    s3_key       TEXT NOT NULL,                          -- full variant key
    thumb_s3_key TEXT NOT NULL,                          -- thumbnail variant key
    content_type TEXT NOT NULL,                          -- no CHECK: Go owns the allowlist
    byte_size    INTEGER NOT NULL,
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    caption      TEXT,                                   -- nullable
    position     INTEGER NOT NULL,                       -- 0-based, NOT unique (see header)
    created_at   DATETIME NOT NULL,
    updated_at   DATETIME NOT NULL,
    deleted_at   DATETIME                                -- nullable soft delete; NULL = live
);

-- Per-activity ordered read; trailing id backs the cover window function and breaks position ties.
CREATE INDEX idx_activity_photo_activity ON activity_photo(activity_id, position, id);
-- Per-user aggregates, newest first.
CREATE INDEX idx_activity_photo_user ON activity_photo(user_id, created_at DESC);
