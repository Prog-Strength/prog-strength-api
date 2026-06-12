-- migrations/018_nutrition_lookup_cache.sql
-- Durable cache of external nutrition lookups (FatSecret / USDA FDC).
--
-- Global, NOT per-user: food data is public, the FatSecret quota is
-- shared across all users, and a global key maximizes hit rate. Rows
-- store per-serving candidates as JSON; quantity scaling happens at
-- read time, so "2 big macs" and "3 big macs" share one upstream hit.
--
-- Freshness: a row whose fetched_at is within the freshness TTL
-- (7 days, code-pinned in internal/nutritionlookup/service.go) serves
-- directly with no external call. Older rows trigger a provider
-- re-pull; success overwrites the row, failure serves the stale row
-- flagged with "stale": true on each candidate.
--
-- Eviction: every cache write piggybacks a sweep deleting rows whose
-- last_used_at is older than 90 days (also code-pinned). Each cache
-- read bumps last_used_at, so foods the users actually eat stay hot
-- forever while one-off lookups age out — bounded growth with no
-- background job. See prog-strength-docs/sows/custom-meal-macro-accuracy.md.

CREATE TABLE IF NOT EXISTS nutrition_lookup_cache (
    query_normalized TEXT PRIMARY KEY,   -- lower-cased, whitespace-collapsed
    candidates_json  TEXT NOT NULL,      -- []Candidate, per-serving values
    fetched_at       DATETIME NOT NULL,  -- last successful provider pull
    last_used_at     DATETIME NOT NULL   -- last cache read (eviction signal)
);

CREATE INDEX idx_nutrition_lookup_cache_last_used
    ON nutrition_lookup_cache(last_used_at);
