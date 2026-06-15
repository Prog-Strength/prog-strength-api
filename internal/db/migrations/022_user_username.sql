-- migrations/021_user_username.sql
-- User-settable public handle. Stored lowercased so uniqueness is
-- case-insensitive by construction. NULL until first set (existing users have
-- none); SQLite permits multiple NULLs in a UNIQUE index, so unset users don't
-- collide. Validated (charset + reserved denylist) at the API write edge.
--
-- The unique index is partial (WHERE deleted_at IS NULL): a soft-deleted
-- account does not reserve its handle, so the freed handle is immediately
-- available to others (the SOW's chosen rename/free semantics). This also
-- keeps the SQLite uniqueness rule identical to the in-memory repository,
-- which excludes deleted users from its collision check.
ALTER TABLE users ADD COLUMN username TEXT;
CREATE UNIQUE INDEX idx_users_username ON users(username) WHERE deleted_at IS NULL;
