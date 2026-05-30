-- migrations/006_nutrition_and_bodyweight.sql
-- The nutrition + bodyweight feature, schema-side. See
-- prog-strength-docs/sows/daily-nutrition-log.md for the full design.
--
-- Five new tables ship in one migration even though the feature
-- rolls out in phases (Phase 1: pantry + log, Phase 2: recipes,
-- Phase 3: bodyweight). Defining the full schema up front lets the
-- write paths land incrementally without subsequent migrations
-- having to relax NOT NULL or rewrite CHECK constraints — the only
-- thing the later phases need is application code.
--
-- Soft delete (deleted_at NULL = active) is used everywhere except
-- recipe_items, which is fully owned by its parent recipe via
-- ON DELETE CASCADE.

-- Pantry: user's saved food items, per-serving macros + descriptive
-- serving unit. Read-mostly; referenced by every log entry that
-- carries a pantry_item_id.
CREATE TABLE IF NOT EXISTS pantry_items (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    calories REAL NOT NULL CHECK(calories >= 0),
    protein_g REAL NOT NULL CHECK(protein_g >= 0),
    fat_g REAL NOT NULL CHECK(fat_g >= 0),
    carbs_g REAL NOT NULL CHECK(carbs_g >= 0),
    -- Serving magnitude + unit are descriptive. Math only cares
    -- about per-serving macros; quantity on the log entry multiplies
    -- through. "5 eggs" is quantity=5 against an item with serving_size=1 unit="egg".
    serving_size REAL NOT NULL CHECK(serving_size > 0),
    serving_unit TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);

-- Drives the pantry list view + search-as-you-type. Partial index
-- keeps soft-deleted rows out of every query path that doesn't
-- explicitly opt in.
CREATE INDEX idx_pantry_user_name
    ON pantry_items(user_id, name) WHERE deleted_at IS NULL;

-- Recipes: named bag of (pantry_item, quantity) pairs. Macros are
-- derived from components at read + log time — nothing macro-shaped
-- lives on this row.
CREATE TABLE IF NOT EXISTS recipes (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);

CREATE INDEX idx_recipes_user_name
    ON recipes(user_id, name) WHERE deleted_at IS NULL;

-- Recipe components. Hard delete on recipe deletion via CASCADE;
-- pantry-item deletion does NOT propagate (a deleted item leaves
-- the recipe component intact, and the read path flags it).
CREATE TABLE IF NOT EXISTS recipe_items (
    id TEXT PRIMARY KEY,
    recipe_id TEXT NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    pantry_item_id TEXT NOT NULL REFERENCES pantry_items(id),
    quantity REAL NOT NULL CHECK(quantity > 0),
    position INTEGER NOT NULL CHECK(position >= 0),
    created_at DATETIME NOT NULL
);

-- Recipe read path: components in display order. Unique covers the
-- positional invariant — the write path rewrites positions densely
-- from 0 inside a single transaction, so two components in the same
-- recipe never share a slot post-commit.
CREATE UNIQUE INDEX idx_recipe_items_recipe_position
    ON recipe_items(recipe_id, position);

-- Nutrition log: one row per consumption event. Exactly one of
-- (pantry_item_id, recipe_id) must be set, enforced by CHECK. Macros
-- are denormalized at log time so editing a pantry item next week
-- does not retroactively rewrite last Tuesday's totals.
CREATE TABLE IF NOT EXISTS nutrition_log_entries (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    consumed_at DATETIME NOT NULL,
    pantry_item_id TEXT REFERENCES pantry_items(id),
    recipe_id TEXT REFERENCES recipes(id),
    -- Multiplier: servings (pantry-item entry) or batches (recipe entry).
    quantity REAL NOT NULL CHECK(quantity > 0),
    -- Denormalized macros frozen at log time.
    calories REAL NOT NULL CHECK(calories >= 0),
    protein_g REAL NOT NULL CHECK(protein_g >= 0),
    fat_g REAL NOT NULL CHECK(fat_g >= 0),
    carbs_g REAL NOT NULL CHECK(carbs_g >= 0),
    created_at DATETIME NOT NULL,
    deleted_at DATETIME,
    -- Exactly one foreign key is set per row. SQLite's CHECK is row-
    -- level and runs on every write, so a future bug that tried to
    -- insert with neither (or both) set would fail loudly.
    CHECK (
        (pantry_item_id IS NOT NULL AND recipe_id IS NULL) OR
        (pantry_item_id IS NULL AND recipe_id IS NOT NULL)
    )
);

-- Drives "today's entries" (frontend) and per-day aggregation
-- (frontend daily widget + agent's get_daily_macros tool).
CREATE INDEX idx_nutrition_log_user_consumed
    ON nutrition_log_entries(user_id, consumed_at DESC) WHERE deleted_at IS NULL;

-- Bodyweight: one row per measurement. Unit is denormalized per
-- row so a user changing their preferred unit doesn't reinterpret
-- historical readings.
CREATE TABLE IF NOT EXISTS bodyweight_entries (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    weight REAL NOT NULL CHECK(weight > 0),
    unit TEXT NOT NULL CHECK(unit IN ('lb', 'kg')),
    measured_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    deleted_at DATETIME
);

-- Trend chart + agent bodyweight tool.
CREATE INDEX idx_bodyweight_user_measured
    ON bodyweight_entries(user_id, measured_at DESC) WHERE deleted_at IS NULL;
