-- migrations/023_timeline_default_visibility.sql
-- Flip the Timeline's effective default from 'private' to 'friends' so an
-- accepted follower sees a user's ENTIRE shareable history, not just posts
-- created after acceptance. New posts now write 'friends' at the write edge
-- (timeline EnsurePost); this migration widens the already-backfilled rows.
-- Safe: no private-marking UI existed before this SOW, so every 'private'
-- row is a backfilled default, not a user's privacy choice.
UPDATE timeline_post SET visibility = 'friends' WHERE visibility = 'private';
