-- migrations/012_nutrition_log_custom_meals.sql
-- Custom meals: let users log one-off meals that aren't backed by a
-- saved pantry item or recipe. See prog-strength-docs/sows/custom-meals.md.
--
-- Adds a nullable custom_meal_name column and widens the existing
-- two-way exactly-one CHECK (pantry_item_id XOR recipe_id) into a
-- three-way XOR over (pantry_item_id, recipe_id, custom_meal_name).
-- Macros stay denormalized in the existing four columns; quantity is
-- always 1 for custom rows (the user types the totals they ate).
--
-- SQLite has no DROP CONSTRAINT, so this recreates the table (the
-- repo's first table recreation) using the standard create →
-- INSERT…SELECT → DROP → RENAME sequence. The whole migration runs in
-- one transaction (see internal/db/migrate.go). Nothing else
-- references nutrition_log_entries, so the drop/recreate is FK-safe:
-- the copied rows still reference live pantry_items / recipes. The new
-- CHECK admits every existing row (each has pantry_item_id or recipe_id
-- set), so no backfill is needed.

CREATE TABLE nutrition_log_entries_new (
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
    -- Meal bucket on the /nutrition page (migration 007). Default +
    -- CHECK preserved verbatim so the recreated table is identical.
    meal TEXT NOT NULL
        DEFAULT 'snack'
        CHECK(meal IN ('breakfast', 'lunch', 'dinner', 'snack')),
    -- NEW: user-typed name for a one-off meal. Non-NULL only on custom rows.
    custom_meal_name TEXT,
    -- Exactly one source is set per row: a pantry item, a recipe, or a
    -- user-typed custom name. SQLite's CHECK is row-level and runs on
    -- every write, so an insert with zero (or two/three) set fails loudly.
    CHECK (
        (pantry_item_id IS NOT NULL AND recipe_id IS NULL AND custom_meal_name IS NULL) OR
        (pantry_item_id IS NULL AND recipe_id IS NOT NULL AND custom_meal_name IS NULL) OR
        (pantry_item_id IS NULL AND recipe_id IS NULL AND custom_meal_name IS NOT NULL)
    )
);

-- Copy every existing row. custom_meal_name is omitted so existing
-- rows get NULL (they all satisfy the new CHECK via pantry_item_id or
-- recipe_id).
INSERT INTO nutrition_log_entries_new (
    id, user_id, consumed_at,
    pantry_item_id, recipe_id, quantity,
    calories, protein_g, fat_g, carbs_g,
    created_at, deleted_at, meal
)
SELECT
    id, user_id, consumed_at,
    pantry_item_id, recipe_id, quantity,
    calories, protein_g, fat_g, carbs_g,
    created_at, deleted_at, meal
FROM nutrition_log_entries;

DROP TABLE nutrition_log_entries;

ALTER TABLE nutrition_log_entries_new RENAME TO nutrition_log_entries;

-- Recreate the soft-delete index from migration 006.
CREATE INDEX idx_nutrition_log_user_consumed
    ON nutrition_log_entries(user_id, consumed_at DESC) WHERE deleted_at IS NULL;
