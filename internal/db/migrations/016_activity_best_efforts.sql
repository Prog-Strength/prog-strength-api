-- migrations/016_activity_best_efforts.sql
-- Running "best efforts" (running PRs). For every running activity the
-- upload-time summarizer runs a distance-anchored two-pointer sweep over
-- the raw trackpoint stream and records the fastest window of each
-- standard distance (1 mile, 2 mile, 5K, 10K, half marathon, marathon).
-- Each row is "the fastest <distance_key> found inside this activity",
-- so a fast 5K embedded in a longer easy run is captured even though the
-- activity totals far more than 5K.
--
-- The per-user current best at a distance is the MIN(duration_seconds)
-- across this table joined to live (deleted_at IS NULL) running
-- activities; the secondary index serves that grouped scan.
--
-- No user_id column: every read joins activities for the live + sport
-- filter anyway, so a denormalized user_id would only ever duplicate the
-- FK target. The ON DELETE CASCADE is defensive — activities soft-delete
-- via deleted_at, and reads filter on it, so the cascade only fires on a
-- true hard delete (which the app doesn't expose today).
--
-- distance_key is constrained to the six v1 standard distances; the same
-- set is the source of truth in internal/activity/best_efforts.go, and
-- TestStandardDistances_MatchMigrationCheck asserts the two stay in sync.
-- duration_seconds is REAL (not INTEGER) because the sweep interpolates
-- the right edge of the window, producing sub-second precision.

CREATE TABLE activity_best_efforts (
    activity_id TEXT NOT NULL,
    distance_key TEXT NOT NULL,
    duration_seconds REAL NOT NULL,
    PRIMARY KEY (activity_id, distance_key),
    FOREIGN KEY (activity_id) REFERENCES activities(id) ON DELETE CASCADE,
    CHECK(distance_key IN ('1mi', '2mi', '5k', '10k', 'half_marathon', 'marathon'))
);

-- Serves the per-distance MIN(duration_seconds) bests query. The composite
-- PK alone orders by activity_id, which doesn't help the cross-activity
-- scan-by-distance the read path performs.
CREATE INDEX idx_activity_best_efforts_distance
    ON activity_best_efforts(distance_key, duration_seconds);
