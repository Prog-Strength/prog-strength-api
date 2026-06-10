-- migrations/017_user_profile.sql
-- User Profile & Preferences: make the profile user-editable.
--
-- Three new nullable columns on users, all additive (no backfill — existing
-- rows get NULLs):
--   height_cm         REAL  Static body metric in canonical cm; converted at
--                           the display edge. Optional, never inferred.
--   avatar_key        TEXT  S3 object key under the prog-strength-avatars
--                           bucket. NULL means "fall back to the OAuth avatar."
--   oauth_avatar_url  TEXT  The OAuth provider's avatar URL (Google `picture`
--                           claim), captured at signup and opportunistically
--                           refreshed on later logins. Source of the GET /me
--                           avatar fallback when avatar_key is NULL.
--
-- Forward-only: this repo's migrations are embedded and applied in order with
-- no down files (see migrate.go); none exist for prior migrations either.

ALTER TABLE users ADD COLUMN height_cm REAL;
ALTER TABLE users ADD COLUMN avatar_key TEXT;
ALTER TABLE users ADD COLUMN oauth_avatar_url TEXT;
