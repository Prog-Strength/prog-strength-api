-- migrations/032_planned_set_amrap.sql
-- planned_sets.amrap — boolean (0/1) marking a set as AMRAP ("as many reps
-- as possible"): no fixed rep target, the lifter goes to the limit. Defaults
-- to 0 so existing sets remain fixed-rep targets.

ALTER TABLE planned_sets
  ADD COLUMN amrap INTEGER NOT NULL DEFAULT 0;
