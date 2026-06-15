-- migrations/021_beta_allowed_emails.sql
-- Dynamic beta allowlist: move the closed-beta email gate out of the
-- BETA_ALLOWED_EMAILS env var (frozen at boot) and into the database so a
-- tester can be authorized with one admin API call — no secret edit, no
-- redeploy. See prog-strength-docs/sows/dynamic-beta-allowlist.md.
--
-- email is normalized (lower-cased, trimmed) before storage; the PRIMARY
-- KEY gives O(log n) IsAllowed lookups and enforces dedup. added_by is the
-- admin email that added the row, or the sentinel
-- "seed:BETA_ALLOWED_EMAILS" for rows carried over by the one-time boot
-- seed (nullable to accommodate it). note is an optional free-text label.

CREATE TABLE IF NOT EXISTS beta_allowed_emails (
    email     TEXT PRIMARY KEY,
    added_at  DATETIME NOT NULL,
    added_by  TEXT,
    note      TEXT
);
