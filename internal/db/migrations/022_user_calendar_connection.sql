-- user_calendar_connection: opt-in Google Calendar sync credentials + metadata,
-- one row per user. refresh_token_enc/refresh_token_nonce hold the AES-256-GCM
-- encrypted Google refresh token; the key lives outside the DB (CALENDAR_TOKEN_ENC_KEY).
CREATE TABLE IF NOT EXISTS user_calendar_connection (
    user_id             TEXT PRIMARY KEY,
    refresh_token_enc   BLOB NOT NULL,
    refresh_token_nonce BLOB NOT NULL,
    google_calendar_id  TEXT NOT NULL,
    scopes              TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('connected','revoked')),
    connected_at        DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);
