package nutrition

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

// --- Pantry --------------------------------------------------------

func (r *SQLiteRepository) CreatePantryItem(ctx context.Context, p *PantryItem) error {
	if err := p.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()
	p.ID = id.New()
	p.CreatedAt = now
	p.UpdatedAt = now
	p.DeletedAt = nil

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO pantry_items (
			id, user_id, name,
			calories, protein_g, fat_g, carbs_g,
			serving_size, serving_unit,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.UserID, p.Name,
		p.Calories, p.ProteinG, p.FatG, p.CarbsG,
		p.ServingSize, p.ServingUnit,
		p.CreatedAt, p.UpdatedAt)
	return err
}

func (r *SQLiteRepository) GetPantryItem(ctx context.Context, userID, itemID string) (*PantryItem, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, name,
		       calories, protein_g, fat_g, carbs_g,
		       serving_size, serving_unit,
		       created_at, updated_at, deleted_at
		FROM pantry_items
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, itemID, userID)

	p, err := scanPantry(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return p, err
}

func (r *SQLiteRepository) ListPantryItems(ctx context.Context, userID, query string) ([]PantryItem, error) {
	q := strings.TrimSpace(query)
	var (
		rows *sql.Rows
		err  error
	)
	if q == "" {
		rows, err = r.db.QueryContext(ctx, `
			SELECT id, user_id, name,
			       calories, protein_g, fat_g, carbs_g,
			       serving_size, serving_unit,
			       created_at, updated_at, deleted_at
			FROM pantry_items
			WHERE user_id = ? AND deleted_at IS NULL
			ORDER BY LOWER(name) ASC
		`, userID)
	} else {
		// LIKE with a leading + trailing % == "contains, case-insensitive
		// thanks to LOWER() on both sides." Small index won't help; pantry
		// per user stays small.
		needle := "%" + strings.ToLower(q) + "%"
		rows, err = r.db.QueryContext(ctx, `
			SELECT id, user_id, name,
			       calories, protein_g, fat_g, carbs_g,
			       serving_size, serving_unit,
			       created_at, updated_at, deleted_at
			FROM pantry_items
			WHERE user_id = ? AND deleted_at IS NULL
			  AND LOWER(name) LIKE ?
			ORDER BY LOWER(name) ASC
		`, userID, needle)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PantryItem
	for rows.Next() {
		p, err := scanPantry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) UpdatePantryItem(ctx context.Context, p *PantryItem) error {
	if err := p.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()
	p.UpdatedAt = now

	res, err := r.db.ExecContext(ctx, `
		UPDATE pantry_items
		SET name = ?,
		    calories = ?, protein_g = ?, fat_g = ?, carbs_g = ?,
		    serving_size = ?, serving_unit = ?,
		    updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, p.Name,
		p.Calories, p.ProteinG, p.FatG, p.CarbsG,
		p.ServingSize, p.ServingUnit,
		p.UpdatedAt,
		p.ID, p.UserID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) DeletePantryItem(ctx context.Context, userID, itemID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE pantry_items
		SET deleted_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, itemID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanPantry reads one pantry_items row out of a Scanner (Row or Rows).
// Using an interface here lets the same code service both single-row
// QueryRow and multi-row Query loops.
type scanner interface {
	Scan(dest ...any) error
}

func scanPantry(s scanner) (*PantryItem, error) {
	var (
		p         PantryItem
		deletedAt sql.NullTime
	)
	if err := s.Scan(
		&p.ID, &p.UserID, &p.Name,
		&p.Calories, &p.ProteinG, &p.FatG, &p.CarbsG,
		&p.ServingSize, &p.ServingUnit,
		&p.CreatedAt, &p.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		p.DeletedAt = &t
	}
	return &p, nil
}

// --- Nutrition log -------------------------------------------------

func (r *SQLiteRepository) CreateNutritionLogEntry(ctx context.Context, e *NutritionLogEntry) error {
	if e.Quantity <= 0 {
		return ErrQuantityNonPositive
	}
	if !exactlyOneSource(e) {
		return ErrLogEntryReferenceRequired
	}
	if !e.Meal.Valid() {
		return ErrInvalidMeal
	}
	now := r.now().UTC()
	e.ID = id.New()
	e.CreatedAt = now
	e.DeletedAt = nil

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO nutrition_log_entries (
			id, user_id, consumed_at,
			pantry_item_id, recipe_id, custom_meal_name, quantity,
			calories, protein_g, fat_g, carbs_g,
			meal, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.ID, e.UserID, e.ConsumedAt,
		nullableString(e.PantryItemID), nullableString(e.RecipeID), nullableString(e.CustomMealName), e.Quantity,
		e.Calories, e.ProteinG, e.FatG, e.CarbsG,
		string(e.Meal), e.CreatedAt)
	return err
}

func (r *SQLiteRepository) GetNutritionLogEntry(ctx context.Context, userID, entryID string) (*NutritionLogEntry, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, consumed_at,
		       pantry_item_id, recipe_id, custom_meal_name, quantity,
		       calories, protein_g, fat_g, carbs_g,
		       meal, created_at, deleted_at
		FROM nutrition_log_entries
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, entryID, userID)

	e, err := scanLogEntry(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return e, err
}

func (r *SQLiteRepository) ListNutritionLogEntries(ctx context.Context, userID string, since, until *time.Time) ([]NutritionLogEntry, error) {
	// Branch on whichever range bounds the caller supplied. Same
	// pattern the workout repo uses; keeps each query simple and
	// readable rather than building dynamic SQL with placeholders.
	args := []any{userID}
	clauses := []string{"user_id = ?", "deleted_at IS NULL"}
	if since != nil {
		clauses = append(clauses, "consumed_at >= ?")
		args = append(args, *since)
	}
	if until != nil {
		clauses = append(clauses, "consumed_at < ?")
		args = append(args, *until)
	}
	q := `
		SELECT id, user_id, consumed_at,
		       pantry_item_id, recipe_id, custom_meal_name, quantity,
		       calories, protein_g, fat_g, carbs_g,
		       meal, created_at, deleted_at
		FROM nutrition_log_entries
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY consumed_at DESC
	`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NutritionLogEntry
	for rows.Next() {
		e, err := scanLogEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) UpdateNutritionLogEntry(ctx context.Context, e *NutritionLogEntry) error {
	if e.Quantity <= 0 {
		return ErrQuantityNonPositive
	}
	if !exactlyOneSource(e) {
		return ErrLogEntryReferenceRequired
	}
	if !e.Meal.Valid() {
		return ErrInvalidMeal
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE nutrition_log_entries
		SET consumed_at = ?,
		    pantry_item_id = ?, recipe_id = ?, custom_meal_name = ?, quantity = ?,
		    calories = ?, protein_g = ?, fat_g = ?, carbs_g = ?,
		    meal = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, e.ConsumedAt,
		nullableString(e.PantryItemID), nullableString(e.RecipeID), nullableString(e.CustomMealName), e.Quantity,
		e.Calories, e.ProteinG, e.FatG, e.CarbsG,
		string(e.Meal),
		e.ID, e.UserID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) DeleteNutritionLogEntry(ctx context.Context, userID, entryID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE nutrition_log_entries
		SET deleted_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, entryID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) DailyMacros(ctx context.Context, userID string, since, until time.Time, loc *time.Location) ([]DailyMacros, error) {
	// Pull raw rows in [since, until) UTC and group in Go by the
	// user-local calendar date in loc. SQLite's date(consumed_at, ?)
	// modifier takes a fixed UTC offset and is wrong on DST transition
	// days, so the grouping cannot live in SQL.
	rows, err := r.db.QueryContext(ctx, `
		SELECT consumed_at, calories, protein_g, fat_g, carbs_g
		FROM nutrition_log_entries
		WHERE user_id = ?
		  AND deleted_at IS NULL
		  AND consumed_at >= ?
		  AND consumed_at < ?
	`, userID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type accumulator struct {
		cal, p, f, c float64
		count        int
	}
	buckets := map[string]*accumulator{}
	for rows.Next() {
		var consumedAt time.Time
		var cal, p, f, c float64
		if err := rows.Scan(&consumedAt, &cal, &p, &f, &c); err != nil {
			return nil, err
		}
		key := consumedAt.In(loc).Format("2006-01-02")
		a := buckets[key]
		if a == nil {
			a = &accumulator{}
			buckets[key] = a
		}
		a.cal += cal
		a.p += p
		a.f += f
		a.c += c
		a.count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]DailyMacros, 0, len(buckets))
	for date, a := range buckets {
		out = append(out, DailyMacros{
			Date:       date,
			Calories:   a.cal,
			ProteinG:   a.p,
			FatG:       a.f,
			CarbsG:     a.c,
			EntryCount: a.count,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

func scanLogEntry(s scanner) (*NutritionLogEntry, error) {
	var (
		e              NutritionLogEntry
		pantryItemID   sql.NullString
		recipeID       sql.NullString
		customMealName sql.NullString
		meal           string
		deletedAt      sql.NullTime
	)
	if err := s.Scan(
		&e.ID, &e.UserID, &e.ConsumedAt,
		&pantryItemID, &recipeID, &customMealName, &e.Quantity,
		&e.Calories, &e.ProteinG, &e.FatG, &e.CarbsG,
		&meal, &e.CreatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if pantryItemID.Valid {
		s := pantryItemID.String
		e.PantryItemID = &s
	}
	if recipeID.Valid {
		s := recipeID.String
		e.RecipeID = &s
	}
	if customMealName.Valid {
		s := customMealName.String
		e.CustomMealName = &s
	}
	e.Meal = MealType(meal)
	if deletedAt.Valid {
		t := deletedAt.Time
		e.DeletedAt = &t
	}
	return &e, nil
}

// nullableString converts *string to a value the SQLite driver
// stores as NULL when nil or empty.
func nullableString(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

// --- Recipes -------------------------------------------------------

func (r *SQLiteRepository) CreateRecipe(ctx context.Context, rec *Recipe) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()
	rec.ID = id.New()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	rec.DeletedAt = nil

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO recipes (id, user_id, name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, rec.ID, rec.UserID, rec.Name, rec.CreatedAt, rec.UpdatedAt); err != nil {
		return err
	}
	if err := insertRecipeComponentsTx(ctx, tx, rec, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLiteRepository) GetRecipe(ctx context.Context, userID, recipeID string) (*Recipe, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, created_at, updated_at, deleted_at
		FROM recipes
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, recipeID, userID)
	rec, err := scanRecipe(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	components, err := r.loadRecipeComponents(ctx, recipeID)
	if err != nil {
		return nil, err
	}
	rec.Components = components
	return rec, nil
}

func (r *SQLiteRepository) ListRecipes(ctx context.Context, userID string) ([]Recipe, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, name, created_at, updated_at, deleted_at
		FROM recipes
		WHERE user_id = ? AND deleted_at IS NULL
		ORDER BY LOWER(name) ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var recipes []Recipe
	for rows.Next() {
		rec, err := scanRecipe(rows)
		if err != nil {
			return nil, err
		}
		recipes = append(recipes, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Component lookup per recipe. Cheap at single-user scale; if it
	// shows up as a hot path later, batch with a single IN-clause
	// query that pre-sorts by (recipe_id, position).
	for i := range recipes {
		comps, err := r.loadRecipeComponents(ctx, recipes[i].ID)
		if err != nil {
			return nil, err
		}
		recipes[i].Components = comps
	}
	return recipes, nil
}

func (r *SQLiteRepository) UpdateRecipe(ctx context.Context, rec *Recipe) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()
	rec.UpdatedAt = now

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		UPDATE recipes
		SET name = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, rec.Name, rec.UpdatedAt, rec.ID, rec.UserID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}

	// Replace component set: delete all then insert the new ones.
	// Inside the transaction so a reader never sees a partial state.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM recipe_items WHERE recipe_id = ?
	`, rec.ID); err != nil {
		return err
	}
	if err := insertRecipeComponentsTx(ctx, tx, rec, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLiteRepository) DeleteRecipe(ctx context.Context, userID, recipeID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE recipes
		SET deleted_at = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, recipeID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) ComputeRecipeMacros(ctx context.Context, userID, recipeID string) (RecipeMacros, error) {
	// Verify the recipe exists + belongs to the user before doing the
	// macro math. Spares us a cross-user macro leak via a guessed
	// recipe ID even if the join below would silently return zero.
	var ownerCheck int
	if err := r.db.QueryRowContext(ctx, `
		SELECT 1 FROM recipes
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		LIMIT 1
	`, recipeID, userID).Scan(&ownerCheck); err != nil {
		if err == sql.ErrNoRows {
			return RecipeMacros{}, ErrNotFound
		}
		return RecipeMacros{}, err
	}

	// Sum the per-component contributions: quantity × the pantry
	// item's per-serving macros. Reads pantry items regardless of
	// deleted_at so a soft-deleted item still contributes — the
	// recipe's macros are independent of the pantry list view.
	row := r.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(ri.quantity * pi.calories), 0),
			COALESCE(SUM(ri.quantity * pi.protein_g), 0),
			COALESCE(SUM(ri.quantity * pi.fat_g), 0),
			COALESCE(SUM(ri.quantity * pi.carbs_g), 0)
		FROM recipe_items ri
		JOIN pantry_items pi ON pi.id = ri.pantry_item_id
		WHERE ri.recipe_id = ? AND pi.user_id = ?
	`, recipeID, userID)
	var m RecipeMacros
	if err := row.Scan(&m.Calories, &m.ProteinG, &m.FatG, &m.CarbsG); err != nil {
		return RecipeMacros{}, err
	}
	return m, nil
}

// insertRecipeComponentsTx writes the recipe's components inside the
// given transaction, assigning IDs + positions densely from 0.
// Shared by Create and Update; the caller is responsible for clearing
// prior rows before calling it from Update.
func insertRecipeComponentsTx(ctx context.Context, tx *sql.Tx, rec *Recipe, now time.Time) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO recipe_items (
			id, recipe_id, pantry_item_id, quantity, position, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i := range rec.Components {
		rec.Components[i].ID = id.New()
		rec.Components[i].Position = i
		rec.Components[i].CreatedAt = now
		c := rec.Components[i]
		if _, err := stmt.ExecContext(ctx, c.ID, rec.ID, c.PantryItemID, c.Quantity, c.Position, c.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *SQLiteRepository) loadRecipeComponents(ctx context.Context, recipeID string) ([]RecipeItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, pantry_item_id, quantity, position, created_at
		FROM recipe_items
		WHERE recipe_id = ?
		ORDER BY position ASC
	`, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecipeItem
	for rows.Next() {
		var c RecipeItem
		if err := rows.Scan(&c.ID, &c.PantryItemID, &c.Quantity, &c.Position, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanRecipe(s scanner) (*Recipe, error) {
	var (
		r         Recipe
		deletedAt sql.NullTime
	)
	if err := s.Scan(&r.ID, &r.UserID, &r.Name, &r.CreatedAt, &r.UpdatedAt, &deletedAt); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		r.DeletedAt = &t
	}
	return &r, nil
}
