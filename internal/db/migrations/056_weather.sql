-- migrations/056_weather.sql
-- Weather tile storage: saved locations, the daily provider-call budget
-- ledger, and the global reading/geocode cache.
-- See sows/weather-dashboard-tile.md.
--
-- user_weather_locations carries NO UNIQUE(user_id, position): writes are a
-- whole-list replace that rewrites positions in one transaction, and a unique
-- constraint would fight the intermediate states. The 5-location cap is
-- enforced in Go (migration 049's precedent: the Go layer is the source of
-- truth, SQL carries no CHECK).
CREATE TABLE user_weather_locations (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    label      TEXT NOT NULL,
    country    TEXT NOT NULL,
    state      TEXT,
    lat        REAL NOT NULL,
    lon        REAL NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE INDEX idx_user_weather_locations_user
    ON user_weather_locations(user_id, position);

-- One row per UTC day, account-level (Prog Strength is single-user by design;
-- the budget is a constraint against OpenWeather, not against a user). Rows
-- are never swept — the history is the audit trail for spend. Durable SQLite
-- rather than a Prometheus counter so a deploy can never hand the integration
-- a fresh allowance (the WHOOP counter postmortem).
CREATE TABLE weather_call_budget (
    usage_date TEXT PRIMARY KEY,           -- YYYY-MM-DD in UTC
    calls_used INTEGER NOT NULL DEFAULT 0, -- monotonically increasing reservations
    updated_at TIMESTAMP NOT NULL
);

-- Global reading/geocode cache, exactly like nutrition_lookup_cache: Denver
-- is Denver regardless of who asked. Eviction piggybacks on write, sweeping
-- rows unused for 90 days (longer than the 30-day geocoding TTL so a geocode
-- row is never evicted at the exact moment it goes stale).
CREATE TABLE weather_cache (
    cache_key    TEXT PRIMARY KEY,
    payload_json TEXT NOT NULL,   -- normalized reading, not the raw provider body
    fetched_at   DATETIME NOT NULL,
    last_used_at DATETIME NOT NULL
);

CREATE INDEX idx_weather_cache_last_used
    ON weather_cache(last_used_at);
