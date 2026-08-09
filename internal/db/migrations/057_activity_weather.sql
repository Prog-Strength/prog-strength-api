-- migrations/057_activity_weather.sql
-- Activity Weather Conditions: the conditions at the start of one outdoor GPS
-- activity, captured once and stored forever. See
-- sows/activity-weather-conditions.md.
--
-- One row per ATTEMPTED activity, not per successful reading: `status` is what
-- stops the import path and the backfill from re-spending budget on an activity
-- that has no answer. Historical weather is immutable, so this is a store and
-- not a cache — no TTL column, no eviction, and deliberately not weather_cache.
--
-- A 1:1 table rather than fourteen columns on `activities`: they are meaningful
-- only for outdoor GPS activities, would sit NULL on every lift and treadmill
-- run, and `activities` is already wide and read on every list page.
--
-- No index beyond the primary key. Every read is a point lookup by activity_id,
-- and the backfill's "what is left" query is an anti-join that the PK serves.
CREATE TABLE activity_weather (
    activity_id  TEXT PRIMARY KEY REFERENCES activities(id) ON DELETE CASCADE,
    status       TEXT NOT NULL CHECK(status IN ('ok','no_coordinates','unavailable')),
    lat          REAL,      -- the coordinate actually used; NULL for no_coordinates
    lon          REAL,
    observed_at  TIMESTAMP, -- the provider's observation hour, UTC; NULL when not ok
    temp_c       REAL,
    feels_like_c REAL,
    dew_point_c  REAL,
    humidity     INTEGER,
    wind_kmh     REAL,
    wind_deg     INTEGER,
    precip_mm    REAL,
    condition    TEXT,
    icon         TEXT,
    fetched_at   TIMESTAMP NOT NULL  -- when we asked, as distinct from observed_at
);
