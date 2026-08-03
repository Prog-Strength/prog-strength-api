-- migrations/051_activity_photo_pending.sql
-- Photo upload moves off the request path: the client PUTs the original
-- straight to S3 and a background worker produces the stored objects. See
-- prog-strength-docs/sows/photo-upload-direct-to-s3.md.
--
-- Additive-only: four ADD COLUMNs plus one index, no rebuild-and-copy. The
-- columns 047 declared NOT NULL (s3_key, thumb_s3_key, byte_size, width,
-- height) stay NOT NULL — SQLite cannot drop a NOT NULL in place, and the
-- rebuild lesson from 042/045 is not worth re-learning here. Instead the
-- reserve step writes the same placeholders the video path established
-- (s3_key = '', dimensions = 0) and the worker fills them in at commit.
--
-- status is the three-phase upload's state:
--   'pending'    reserved; the client is uploading the original to S3
--   'processing' commit confirmed the object landed; the worker holds it
--   'ready'      stripped photo + thumb written; the row is renderable
--   'failed'     the worker gave up; the row is excluded from reads
-- Reads resolve URLs for 'ready' only; 'processing' renders as a placeholder.
-- No CHECK constraint, for the same reason content_type has none in 047 —
-- SQLite cannot widen a table-level CHECK in place, and Go owns the values.
--
-- DEFAULT 'ready' is what makes this additive rather than a backfill: every
-- existing row was written by the synchronous path and is, by definition,
-- already done. Nothing to migrate.
ALTER TABLE activity_photo ADD COLUMN status TEXT NOT NULL DEFAULT 'ready';

-- The STAGING object the client PUT to, under the bucket's uploads/ prefix.
-- It still carries the source's EXIF (including GPS), so it is never presigned
-- for GET and is DELETEd — not lifecycle-tagged — as soon as the worker has
-- written the stripped copy. Held in its own column, distinct from s3_key (the
-- serving object), precisely so the two can never be confused: one is safe to
-- hand a browser and the other is not.
--
-- Nullable: rows written by the old synchronous path never had one.
ALTER TABLE activity_photo ADD COLUMN upload_s3_key TEXT;

-- Attempt accounting, so a poison image cannot be retried forever. Transient
-- failures (S3, disk) increment and retry with backoff; the cap sends the row
-- to 'failed' with last_error recorded for diagnosis.
ALTER TABLE activity_photo ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE activity_photo ADD COLUMN last_error TEXT;

-- Backs both the worker's claim query (status = 'processing', oldest first)
-- and the reaper's sweep for abandoned reservations (status = 'pending' with
-- created_at past the presign TTL). Mirrors idx_activity_video_status from
-- 048, which serves exactly the same two readers.
CREATE INDEX idx_activity_photo_status ON activity_photo(status, created_at);
