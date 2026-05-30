package nutrition

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation. Holds
// state in maps protected by a single RW mutex — same pattern as the
// workout package's MemoryRepository.
type MemoryRepository struct {
	mu      sync.RWMutex
	pantry  map[string]*PantryItem        // id → item
	log     map[string]*NutritionLogEntry // id → entry
	recipes map[string]*Recipe            // id → recipe (with components)
	nowFunc func() time.Time              // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		pantry:  make(map[string]*PantryItem),
		log:     make(map[string]*NutritionLogEntry),
		recipes: make(map[string]*Recipe),
		nowFunc: time.Now,
	}
}

// --- Pantry --------------------------------------------------------

func (r *MemoryRepository) CreatePantryItem(ctx context.Context, p *PantryItem) error {
	if err := p.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc().UTC()
	p.ID = id.New()
	p.CreatedAt = now
	p.UpdatedAt = now
	p.DeletedAt = nil

	stored := *p
	r.pantry[p.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetPantryItem(ctx context.Context, userID, id string) (*PantryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.pantry[id]
	if !ok || p.UserID != userID || p.DeletedAt != nil {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *MemoryRepository) ListPantryItems(ctx context.Context, userID, query string) ([]PantryItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	needle := strings.ToLower(strings.TrimSpace(query))
	var out []PantryItem
	for _, p := range r.pantry {
		if p.UserID != userID || p.DeletedAt != nil {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(p.Name), needle) {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (r *MemoryRepository) UpdatePantryItem(ctx context.Context, p *PantryItem) error {
	if err := p.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.pantry[p.ID]
	if !ok || existing.UserID != p.UserID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = now
	p.DeletedAt = nil

	stored := *p
	r.pantry[p.ID] = &stored
	return nil
}

func (r *MemoryRepository) DeletePantryItem(ctx context.Context, userID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.pantry[id]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	existing.DeletedAt = &now
	return nil
}

// --- Nutrition log -------------------------------------------------

func (r *MemoryRepository) CreateNutritionLogEntry(ctx context.Context, e *NutritionLogEntry) error {
	if e.Quantity <= 0 {
		return ErrQuantityNonPositive
	}
	// Schema CHECK requires exactly one of pantry_item_id / recipe_id.
	if !exactlyOneRef(e) {
		return ErrLogEntryReferenceRequired
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc().UTC()
	e.ID = id.New()
	e.CreatedAt = now
	e.DeletedAt = nil

	stored := *e
	r.log[e.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetNutritionLogEntry(ctx context.Context, userID, id string) (*NutritionLogEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	e, ok := r.log[id]
	if !ok || e.UserID != userID || e.DeletedAt != nil {
		return nil, ErrNotFound
	}
	cp := *e
	return &cp, nil
}

func (r *MemoryRepository) ListNutritionLogEntries(ctx context.Context, userID string, since, until *time.Time) ([]NutritionLogEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []NutritionLogEntry
	for _, e := range r.log {
		if e.UserID != userID || e.DeletedAt != nil {
			continue
		}
		if since != nil && e.ConsumedAt.Before(*since) {
			continue
		}
		if until != nil && !e.ConsumedAt.Before(*until) {
			continue
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ConsumedAt.After(out[j].ConsumedAt)
	})
	return out, nil
}

func (r *MemoryRepository) UpdateNutritionLogEntry(ctx context.Context, e *NutritionLogEntry) error {
	if e.Quantity <= 0 {
		return ErrQuantityNonPositive
	}
	if !exactlyOneRef(e) {
		return ErrLogEntryReferenceRequired
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.log[e.ID]
	if !ok || existing.UserID != e.UserID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	e.CreatedAt = existing.CreatedAt
	e.DeletedAt = nil

	stored := *e
	r.log[e.ID] = &stored
	return nil
}

func (r *MemoryRepository) DeleteNutritionLogEntry(ctx context.Context, userID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.log[id]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	existing.DeletedAt = &now
	return nil
}

func (r *MemoryRepository) DailyMacros(ctx context.Context, userID string, since, until time.Time) ([]DailyMacros, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Aggregate per UTC date bucket. Empty days are omitted; callers
	// fill gaps client-side if they need a dense series.
	type accumulator struct {
		cal, p, f, c float64
		count        int
	}
	buckets := map[string]*accumulator{}
	for _, e := range r.log {
		if e.UserID != userID || e.DeletedAt != nil {
			continue
		}
		if e.ConsumedAt.Before(since) || !e.ConsumedAt.Before(until) {
			continue
		}
		key := e.ConsumedAt.UTC().Format("2006-01-02")
		a := buckets[key]
		if a == nil {
			a = &accumulator{}
			buckets[key] = a
		}
		a.cal += e.Calories
		a.p += e.ProteinG
		a.f += e.FatG
		a.c += e.CarbsG
		a.count++
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

// exactlyOneRef returns true when the entry has exactly one of
// PantryItemID and RecipeID set, mirroring the schema CHECK.
func exactlyOneRef(e *NutritionLogEntry) bool {
	hasPantry := e.PantryItemID != nil && *e.PantryItemID != ""
	hasRecipe := e.RecipeID != nil && *e.RecipeID != ""
	return hasPantry != hasRecipe
}

// --- Recipes -------------------------------------------------------

func (r *MemoryRepository) CreateRecipe(ctx context.Context, rec *Recipe) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc().UTC()
	rec.ID = id.New()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	rec.DeletedAt = nil
	// Assign positions densely from 0 + per-component IDs.
	for i := range rec.Components {
		rec.Components[i].ID = id.New()
		rec.Components[i].Position = i
		rec.Components[i].CreatedAt = now
	}

	stored := *rec
	// Defensive copy of the component slice so callers can't mutate
	// our state through the slice header they passed in.
	stored.Components = append([]RecipeItem(nil), rec.Components...)
	r.recipes[rec.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetRecipe(ctx context.Context, userID, id string) (*Recipe, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.recipes[id]
	if !ok || rec.UserID != userID || rec.DeletedAt != nil {
		return nil, ErrNotFound
	}
	cp := *rec
	cp.Components = append([]RecipeItem(nil), rec.Components...)
	return &cp, nil
}

func (r *MemoryRepository) ListRecipes(ctx context.Context, userID string) ([]Recipe, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Recipe
	for _, rec := range r.recipes {
		if rec.UserID != userID || rec.DeletedAt != nil {
			continue
		}
		cp := *rec
		cp.Components = append([]RecipeItem(nil), rec.Components...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (r *MemoryRepository) UpdateRecipe(ctx context.Context, rec *Recipe) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.recipes[rec.ID]
	if !ok || existing.UserID != rec.UserID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	rec.CreatedAt = existing.CreatedAt
	rec.UpdatedAt = now
	rec.DeletedAt = nil
	for i := range rec.Components {
		rec.Components[i].ID = id.New()
		rec.Components[i].Position = i
		rec.Components[i].CreatedAt = now
	}

	stored := *rec
	stored.Components = append([]RecipeItem(nil), rec.Components...)
	r.recipes[rec.ID] = &stored
	return nil
}

func (r *MemoryRepository) DeleteRecipe(ctx context.Context, userID, recipeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.recipes[recipeID]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	existing.DeletedAt = &now
	return nil
}

func (r *MemoryRepository) ComputeRecipeMacros(ctx context.Context, userID, recipeID string) (RecipeMacros, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.recipes[recipeID]
	if !ok || rec.UserID != userID || rec.DeletedAt != nil {
		return RecipeMacros{}, ErrNotFound
	}

	var totals RecipeMacros
	for _, c := range rec.Components {
		// Soft-deleted pantry items are still read for macro math.
		// The component's macros are what they always were; the
		// deletion only hides the item from the pantry list, not
		// from recipes that already reference it.
		item, ok := r.pantry[c.PantryItemID]
		if !ok || item.UserID != userID {
			// True cross-user or hard-missing reference — treat as
			// zero contribution rather than failing the whole macro
			// computation. ListRecipes / GetRecipe surface the
			// stale-component state to the UI via a separate path.
			continue
		}
		totals.Calories += c.Quantity * item.Calories
		totals.ProteinG += c.Quantity * item.ProteinG
		totals.FatG += c.Quantity * item.FatG
		totals.CarbsG += c.Quantity * item.CarbsG
	}
	return totals, nil
}
