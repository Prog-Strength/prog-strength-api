-- user_whoop_connection: one row per user holding the encrypted Whoop OAuth
-- token pair + connection metadata. Mirrors user_calendar_connection (026)
-- with Whoop-specific additions: whoop_user_id (the inbound-webhook join key),
-- a persisted access token (Whoop access tokens live ~1h), and single-use
-- refresh rotation. Token blobs are AES-256-GCM; the key lives outside the DB
-- (WHOOP_TOKEN_ENC_KEY).
CREATE TABLE IF NOT EXISTS user_whoop_connection (
    user_id             TEXT PRIMARY KEY,
    whoop_user_id       INTEGER NOT NULL UNIQUE,
    access_token_enc    BLOB NOT NULL,
    access_token_nonce  BLOB NOT NULL,
    refresh_token_enc   BLOB NOT NULL,
    refresh_token_nonce BLOB NOT NULL,
    token_expires_at    DATETIME NOT NULL,
    scopes              TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('connected','revoked','error')),
    connected_at        DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);
