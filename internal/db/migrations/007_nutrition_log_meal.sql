-- migrations/007_nutrition_log_meal.sql
-- Add meal categorization to nutrition log entries. The /nutrition
-- page on the frontend will group entries into Breakfast / Lunch /
-- Dinner / Snacks sections with per-meal macro subtotals, which is
-- what lifters actually think in terms of when they audit a day's
-- intake. The agent picks a meal value when it logs on the user's
-- behalf, so the agent's chat-flow ergonomics improve too.
--
-- ALTER ADD COLUMN with a DEFAULT fills every existing row at
-- migration time, which lets us declare NOT NULL up front. The
-- default is 'snack' rather than a time-of-day heuristic because
-- timestamps are UTC and we don't store the user's local timezone
-- yet — guessing breakfast vs dinner from a UTC hour is wrong for
-- half the world. The few existing rows the (single beta) user
-- cares about can be recategorized via the UI's edit flow.
--
-- The CHECK constraint mirrors the Go MealType enum
-- (internal/nutrition/meal.go). New rows with anything outside the
-- four values fail at the DB layer; the handler validates first so
-- the user sees a clean 400, not a 500.

ALTER TABLE nutrition_log_entries
    ADD COLUMN meal TEXT NOT NULL
        DEFAULT 'snack'
        CHECK(meal IN ('breakfast', 'lunch', 'dinner', 'snack'));
