-- migrations/048_activity_videos.sql
-- Activity Videos: attach uploaded videos (original object + poster JPEG) to any
-- activity. See prog-strength-docs/sows/activity-videos.md.
--
-- Additive-only: one CREATE TABLE plus three indexes, no rebuild-and-copy. The
-- same expand-only shape as 047_activity_photos.sql, which this table otherwise
-- mirrors closely — read them side by side.
--
-- Four columns differ from activity_photo, and each one earns its place:
--
--   poster_s3_key is NULLABLE. The poster is generated client-side (no ffmpeg in
--   a pure-Go pipeline), so it can fail on a codec the browser won't decode. A
--   video whose poster never materialized is still the keepsake the user came
--   for and must not be rejected or lost — the strip renders a placeholder.
--
--   status is the two-phase upload's state. The bytes go straight to S3 via a
--   presigned URL, so a row exists BEFORE its object does: 'pending' on reserve,
--   'ready' once the complete call confirms the object via HEAD. Reads return
--   only 'ready'. No CHECK constraint, for the same reason content_type has none.
--
--   duration_seconds / width / height are ADVISORY. They come from the client's
--   <video> element because nothing server-side decodes the container. They
--   drive display only (aspect-ratio boxes, a duration badge) and must never
--   gate policy — byte_size is the one the server re-derives from S3 at commit.
--
-- content_type carries NO CHECK: SQLite cannot widen a table-level CHECK in
-- place (the rebuild lesson from 042/045) and the accepted container list will
-- grow. Go owns the allowlist so widening never costs a migration. Unlike
-- photos, this value is NOT normalized on write — nothing re-encodes, so it
-- records the container actually stored.
--
-- position is 0-based and deliberately NOT unique: reorder rewrites the whole
-- set in one transaction, and enforcing uniqueness would make that rewrite's
-- intermediate states illegal. Ties break on id, the trailing index column.
--
-- Soft delete on both sides: deleted_at NULL = live. The activities(id) FK still
-- CASCADEs a hard parent delete so no orphan rows survive.

CREATE TABLE activity_video (
    id               TEXT PRIMARY KEY,
    activity_id      TEXT NOT NULL REFERENCES activities(id) ON DELETE CASCADE,
    user_id          TEXT NOT NULL,                      -- denormalized owner for per-user aggregates
    s3_key           TEXT NOT NULL,                      -- stored original, byte-identical to the upload
    poster_s3_key    TEXT,                               -- nullable: poster generation is best-effort
    content_type     TEXT NOT NULL,                      -- no CHECK: Go owns the allowlist
    byte_size        INTEGER NOT NULL,                   -- server-confirmed via HEAD at commit, not client-claimed
    duration_seconds REAL,                               -- advisory, client-reported
    width            INTEGER,                            -- advisory, client-reported
    height           INTEGER,                            -- advisory, client-reported
    status           TEXT NOT NULL,                      -- 'pending' | 'ready'; no CHECK (see header)
    caption          TEXT,                               -- nullable
    position         INTEGER NOT NULL,                   -- 0-based, NOT unique (see header)
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    deleted_at       DATETIME                            -- nullable soft delete; NULL = live
);

-- Per-activity ordered read; trailing id breaks position ties.
CREATE INDEX idx_activity_video_activity ON activity_video(activity_id, position, id);
-- Per-user aggregates, newest first.
CREATE INDEX idx_activity_video_user ON activity_video(user_id, created_at DESC);
-- Backs the pending-row reaper: find abandoned reservations without a table scan.
CREATE INDEX idx_activity_video_status ON activity_video(status, created_at);
