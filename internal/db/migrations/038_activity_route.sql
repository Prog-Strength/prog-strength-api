-- migrations/038_activity_route.sql
-- Trail Map Rendering: capture GPS route geometry at TCX ingest.
--   activity_trackpoints.latitude / .longitude  WGS84 degrees, truncated to
--                        6 decimals on write (~10 cm). NULL when the source
--                        trackpoint lacked <Position> (indoor / no-GPS, or a
--                        kept downsample sample that had no position).
--   activities.route_geojson  serialized GeoJSON Feature (MultiLineString +
--                        bounds) for the simplified route. NULL when fewer
--                        than two positioned points remain after gap-splitting.
-- Additive-only, same expand-only pattern as 036_activity_environment.sql: no
-- table rebuild, no backfill. Existing rows keep NULL coords / NULL route
-- (correct "no map"). New uploads after deploy populate both.
-- See prog-strength-docs/sows/sow-trail-map.md.

ALTER TABLE activity_trackpoints ADD COLUMN latitude REAL;
ALTER TABLE activity_trackpoints ADD COLUMN longitude REAL;
ALTER TABLE activities ADD COLUMN route_geojson TEXT;
