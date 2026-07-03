-- migrations/036_activity_environment.sql
-- Treadmill Run Calibration: add two general, cross-cutting columns to
-- activities.
--   environment          'outdoor' | 'indoor' — whether the activity was
--                        recorded with GPS (outdoor) or without (indoor /
--                        treadmill). General to any distance-based sport,
--                        not running-only.
--   raw_distance_meters  the distance as originally ingested, never mutated
--                        by calibration — enables provenance ("3.10 -> 3.00
--                        mi") and reset-to-original without an S3 read.
-- See prog-strength-docs/sows/treadmill-run-calibration.md.
--
-- SQLite ALTER TABLE ADD COLUMN accepts a column-level CHECK and a constant
-- DEFAULT, so both columns are added in place — no table rebuild. Every
-- existing row defaults environment='outdoor'. raw_distance_meters is added
-- NOT NULL with a constant 0 default (SQLite requires a non-null constant
-- default when adding a NOT NULL column to a populated table) and then
-- backfilled from the current distance_meters so provenance is exact.

ALTER TABLE activities
    ADD COLUMN environment TEXT NOT NULL DEFAULT 'outdoor'
        CHECK (environment IN ('outdoor', 'indoor'));

ALTER TABLE activities
    ADD COLUMN raw_distance_meters REAL NOT NULL DEFAULT 0;

UPDATE activities SET raw_distance_meters = distance_meters;
