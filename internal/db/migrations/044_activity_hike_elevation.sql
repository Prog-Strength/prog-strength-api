-- migrations/044_activity_hike_elevation.sql
-- Hiking is the fifth endurance-shaped type. It reuses the shared endurance
-- detail shape, so this migration only (a) creates activity_hike_details and
-- (b) widens the shared shape with the elevation triple (loss/high/low) on
-- every existing detail table. No CHECK on activities.activity_type — the Go
-- registry owns the type enum (see migration 042). The elevation columns are
-- nullable with no default: a NULL means "no altitude in the source track",
-- distinct from a genuinely flat 0.

CREATE TABLE activity_hike_details (
    activity_id TEXT PRIMARY KEY REFERENCES activities(id) ON DELETE CASCADE,
    distance_meters REAL NOT NULL,
    raw_distance_meters REAL NOT NULL DEFAULT 0,
    avg_pace_sec_per_km REAL,
    best_pace_sec_per_km REAL,
    elevation_gain_meters REAL,
    elevation_loss_meters REAL,
    elevation_high_meters REAL,
    elevation_low_meters REAL,
    environment TEXT NOT NULL DEFAULT 'outdoor' CHECK(environment IN ('outdoor','indoor')),
    route_geojson TEXT
);

ALTER TABLE activity_run_details ADD COLUMN elevation_loss_meters REAL;
ALTER TABLE activity_run_details ADD COLUMN elevation_high_meters REAL;
ALTER TABLE activity_run_details ADD COLUMN elevation_low_meters REAL;

ALTER TABLE activity_walk_details ADD COLUMN elevation_loss_meters REAL;
ALTER TABLE activity_walk_details ADD COLUMN elevation_high_meters REAL;
ALTER TABLE activity_walk_details ADD COLUMN elevation_low_meters REAL;

ALTER TABLE activity_cycle_details ADD COLUMN elevation_loss_meters REAL;
ALTER TABLE activity_cycle_details ADD COLUMN elevation_high_meters REAL;
ALTER TABLE activity_cycle_details ADD COLUMN elevation_low_meters REAL;

ALTER TABLE activity_other_details ADD COLUMN elevation_loss_meters REAL;
ALTER TABLE activity_other_details ADD COLUMN elevation_high_meters REAL;
ALTER TABLE activity_other_details ADD COLUMN elevation_low_meters REAL;
