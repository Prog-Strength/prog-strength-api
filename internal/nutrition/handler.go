package nutrition

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/daterange"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler exposes the HTTP surface for pantry items and nutrition log
// entries. Phase 1 ships pantry + log + per-day aggregation; recipes
// and bodyweight ship in later phases as new handlers (or new packages
// in the bodyweight case).
type Handler struct {
	repo Repository
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

// Mount registers routes under the given router. Callers are expected
// to have already wrapped the router in auth.RequireUser middleware
// — these handlers read the user ID from request context and assume
// it's present.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/pantry-items", func(r chi.Router) {
		r.Get("/", h.listPantryItems)
		r.Post("/", h.createPantryItem)
		r.Get("/{id}", h.getPantryItem)
		r.Put("/{id}", h.updatePantryItem)
		r.Delete("/{id}", h.deletePantryItem)
	})
	r.Route("/nutrition-log", func(r chi.Router) {
		// Registered before /{id} so chi matches the literal segment
		// rather than interpreting "daily" as a log entry ID.
		r.Get("/daily", h.dailyMacros)
		r.Post("/custom", h.createCustomLogEntry)
		r.Post("/batch", h.createLogEntriesBatch)
		r.Get("/", h.listLogEntries)
		r.Post("/", h.createLogEntry)
		r.Get("/{id}", h.getLogEntry)
		r.Put("/{id}", h.updateLogEntry)
		r.Delete("/{id}", h.deleteLogEntry)
	})
	r.Route("/recipes", func(r chi.Router) {
		r.Get("/", h.listRecipes)
		r.Post("/", h.createRecipe)
		r.Get("/{id}", h.getRecipe)
		r.Put("/{id}", h.updateRecipe)
		r.Delete("/{id}", h.deleteRecipe)
	})
	// Per-user macro targets. Lives on /me/... to match the
	// headline-exercises convention (one row per user, no listing,
	// set-replacement semantics). See
	// prog-strength-docs/sows/daily-macro-goals.md.
	r.Route("/me/macro-goals", func(r chi.Router) {
		r.Get("/", h.getMyMacroGoals)
		r.Put("/", h.putMyMacroGoals)
	})
}

// --- DTOs ----------------------------------------------------------

type pantryItemDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Calories    float64   `json:"calories"`
	ProteinG    float64   `json:"protein_g"`
	FatG        float64   `json:"fat_g"`
	CarbsG      float64   `json:"carbs_g"`
	ServingSize float64   `json:"serving_size"`
	ServingUnit string    `json:"serving_unit"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toPantryDTO(p PantryItem) pantryItemDTO {
	return pantryItemDTO{
		ID:          p.ID,
		Name:        p.Name,
		Calories:    p.Calories,
		ProteinG:    p.ProteinG,
		FatG:        p.FatG,
		CarbsG:      p.CarbsG,
		ServingSize: p.ServingSize,
		ServingUnit: p.ServingUnit,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

type pantryItemRequest struct {
	Name        string  `json:"name"`
	Calories    float64 `json:"calories"`
	ProteinG    float64 `json:"protein_g"`
	FatG        float64 `json:"fat_g"`
	CarbsG      float64 `json:"carbs_g"`
	ServingSize float64 `json:"serving_size"`
	ServingUnit string  `json:"serving_unit"`
}

type logEntryDTO struct {
	ID           string    `json:"id"`
	ConsumedAt   time.Time `json:"consumed_at"`
	PantryItemID *string   `json:"pantry_item_id,omitempty"`
	RecipeID     *string   `json:"recipe_id,omitempty"`
	// CustomMealName is null for pantry/recipe-backed rows. The key is
	// always present (no omitempty) so clients can branch on it.
	CustomMealName *string   `json:"custom_meal_name"`
	Quantity       float64   `json:"quantity"`
	Calories       float64   `json:"calories"`
	ProteinG       float64   `json:"protein_g"`
	FatG           float64   `json:"fat_g"`
	CarbsG         float64   `json:"carbs_g"`
	Meal           string    `json:"meal"`
	CreatedAt      time.Time `json:"created_at"`
}

func toLogDTO(e NutritionLogEntry) logEntryDTO {
	return logEntryDTO{
		ID:             e.ID,
		ConsumedAt:     e.ConsumedAt,
		PantryItemID:   e.PantryItemID,
		RecipeID:       e.RecipeID,
		CustomMealName: e.CustomMealName,
		Quantity:       e.Quantity,
		Calories:       e.Calories,
		ProteinG:       e.ProteinG,
		FatG:           e.FatG,
		CarbsG:         e.CarbsG,
		Meal:           string(e.Meal),
		CreatedAt:      e.CreatedAt,
	}
}

type logEntryRequest struct {
	ConsumedAt   *time.Time `json:"consumed_at"`
	PantryItemID *string    `json:"pantry_item_id"`
	RecipeID     *string    `json:"recipe_id"`
	Quantity     float64    `json:"quantity"`
	// Meal is required on create. Update accepts an empty string to
	// signal "leave the existing meal in place."
	Meal string `json:"meal"`
	// Custom-only fields, valid on update against a custom-meal entry.
	// updateLogEntry 400s if any of these is set against a pantry- or
	// recipe-backed row, since those rows' macros derive from a source.
	Name     *string  `json:"name,omitempty"`
	Calories *float64 `json:"calories,omitempty"`
	ProteinG *float64 `json:"protein_g,omitempty"`
	FatG     *float64 `json:"fat_g,omitempty"`
	CarbsG   *float64 `json:"carbs_g,omitempty"`
}

// customLogEntryRequest is the body for POST /nutrition-log/custom — a
// one-off meal the user typed, not backed by a pantry item or recipe.
type customLogEntryRequest struct {
	Name       string     `json:"name"`
	Calories   float64    `json:"calories"`
	ProteinG   float64    `json:"protein_g"`
	FatG       float64    `json:"fat_g"`
	CarbsG     float64    `json:"carbs_g"`
	Meal       string     `json:"meal"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}

// MaxBatchLogItems caps a single batch request — a runaway-model guard
// well above any real meal. See sows/batch-food-logging.md.
const MaxBatchLogItems = 50

// batchLogItemRequest is one item in a POST /nutrition-log/batch body.
// `kind` is the authoritative selector; the per-kind fields reuse the
// single endpoints' field names.
type batchLogItemRequest struct {
	Kind         string     `json:"kind"`
	PantryItemID *string    `json:"pantry_item_id"`
	RecipeID     *string    `json:"recipe_id"`
	Quantity     float64    `json:"quantity"`
	Name         string     `json:"name"`
	Calories     float64    `json:"calories"`
	ProteinG     float64    `json:"protein_g"`
	FatG         float64    `json:"fat_g"`
	CarbsG       float64    `json:"carbs_g"`
	Meal         string     `json:"meal"`
	ConsumedAt   *time.Time `json:"consumed_at"`
}

type batchLogRequest struct {
	Items []batchLogItemRequest `json:"items"`
}

// batchLogResultDTO is one index-aligned per-item outcome. Entry is set
// when ok; Error is set otherwise. omitempty keeps each object to the
// shape that applies.
type batchLogResultDTO struct {
	Index int          `json:"index"`
	OK    bool         `json:"ok"`
	Entry *logEntryDTO `json:"entry,omitempty"`
	Error string       `json:"error,omitempty"`
}

type batchLogResponseDTO struct {
	Results []batchLogResultDTO `json:"results"`
	Logged  int                 `json:"logged"`
	Failed  int                 `json:"failed"`
}

type dailyMacrosDTO struct {
	Date       string  `json:"date"`
	Calories   float64 `json:"calories"`
	ProteinG   float64 `json:"protein_g"`
	FatG       float64 `json:"fat_g"`
	CarbsG     float64 `json:"carbs_g"`
	EntryCount int     `json:"entry_count"`
}

// --- Pantry handlers -----------------------------------------------

func (h *Handler) createPantryItem(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req pantryItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	item := &PantryItem{
		UserID:      userID,
		Name:        req.Name,
		Calories:    req.Calories,
		ProteinG:    req.ProteinG,
		FatG:        req.FatG,
		CarbsG:      req.CarbsG,
		ServingSize: req.ServingSize,
		ServingUnit: req.ServingUnit,
	}
	if err := h.repo.CreatePantryItem(r.Context(), item); err != nil {
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "create pantry item", err)
		return
	}
	httpresp.Created(w, "created pantry item", toPantryDTO(*item))
}

func (h *Handler) listPantryItems(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	items, err := h.repo.ListPantryItems(r.Context(), userID, r.URL.Query().Get("q"))
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list pantry items", err)
		return
	}
	out := make([]pantryItemDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toPantryDTO(p))
	}
	httpresp.OK(w, "listed pantry items", out)
}

func (h *Handler) getPantryItem(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "pantry item id is required")
		return
	}
	item, err := h.repo.GetPantryItem(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "pantry item not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get pantry item", err)
		return
	}
	httpresp.OK(w, "fetched pantry item", toPantryDTO(*item))
}

func (h *Handler) updatePantryItem(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "pantry item id is required")
		return
	}
	var req pantryItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	item := &PantryItem{
		ID:          id,
		UserID:      userID,
		Name:        req.Name,
		Calories:    req.Calories,
		ProteinG:    req.ProteinG,
		FatG:        req.FatG,
		CarbsG:      req.CarbsG,
		ServingSize: req.ServingSize,
		ServingUnit: req.ServingUnit,
	}
	if err := h.repo.UpdatePantryItem(r.Context(), item); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "pantry item not found")
			return
		}
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "update pantry item", err)
		return
	}
	httpresp.OK(w, "updated pantry item", toPantryDTO(*item))
}

func (h *Handler) deletePantryItem(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "pantry item id is required")
		return
	}
	if err := h.repo.DeletePantryItem(r.Context(), userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "pantry item not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete pantry item", err)
		return
	}
	httpresp.OK(w, "deleted pantry item", nil)
}

// --- Nutrition log handlers ----------------------------------------

func (h *Handler) createLogEntry(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req logEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	entry, err := h.buildLogEntry(r.Context(), userID, req)
	if err != nil {
		writeLogBuildError(w, r, "build log entry", err)
		return
	}
	if err := h.repo.CreateNutritionLogEntry(r.Context(), entry); err != nil {
		httpresp.ServerError(w, r.Context(), "create nutrition log entry", err)
		return
	}
	httpresp.Created(w, "logged consumption", toLogDTO(*entry))
}

// buildLogEntry validates a pantry- or recipe-backed log request and
// returns the unsaved NutritionLogEntry with denormalized macros frozen at
// log time. Caller-facing problems come back as *logBuildError; storage
// failures come back as plain errors. It does not insert.
func (h *Handler) buildLogEntry(ctx context.Context, userID string, req logEntryRequest) (*NutritionLogEntry, error) {
	hasPantry := req.PantryItemID != nil && *req.PantryItemID != ""
	hasRecipe := req.RecipeID != nil && *req.RecipeID != ""
	if hasPantry == hasRecipe {
		return nil, &logBuildError{http.StatusBadRequest, ErrLogEntryReferenceRequired.Error()}
	}
	if req.Quantity <= 0 {
		return nil, &logBuildError{http.StatusBadRequest, ErrQuantityNonPositive.Error()}
	}
	meal := MealType(req.Meal)
	if !meal.Valid() {
		return nil, &logBuildError{http.StatusBadRequest, ErrInvalidMeal.Error()}
	}
	// Derive the denormalized macros at log time from whichever source
	// the request points at. Whatever lands on the entry's macro columns
	// is frozen — future edits to the pantry item or recipe will not
	// retroactively change this entry.
	consumedAt := time.Now().UTC()
	if req.ConsumedAt != nil {
		consumedAt = req.ConsumedAt.UTC()
	}
	entry := &NutritionLogEntry{
		UserID:     userID,
		ConsumedAt: consumedAt,
		Quantity:   req.Quantity,
		Meal:       meal,
	}
	if hasPantry {
		pantry, err := h.repo.GetPantryItem(ctx, userID, *req.PantryItemID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, &logBuildError{http.StatusNotFound, "pantry item not found"}
			}
			return nil, err
		}
		entry.PantryItemID = req.PantryItemID
		entry.Calories = req.Quantity * pantry.Calories
		entry.ProteinG = req.Quantity * pantry.ProteinG
		entry.FatG = req.Quantity * pantry.FatG
		entry.CarbsG = req.Quantity * pantry.CarbsG
	} else {
		macros, err := h.repo.ComputeRecipeMacros(ctx, userID, *req.RecipeID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, &logBuildError{http.StatusNotFound, "recipe not found"}
			}
			return nil, err
		}
		scaled := macros.Scale(req.Quantity)
		entry.RecipeID = req.RecipeID
		entry.Calories = scaled.Calories
		entry.ProteinG = scaled.ProteinG
		entry.FatG = scaled.FatG
		entry.CarbsG = scaled.CarbsG
	}
	return entry, nil
}

// createCustomLogEntry logs a one-off meal the user typed, not backed
// by a pantry item or recipe. The macros land verbatim on the row's
// denormalized columns (no source to derive from) and quantity is
// fixed at 1 — the user types the totals they ate.
func (h *Handler) createCustomLogEntry(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req customLogEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	entry, err := buildCustomLogEntry(userID, req)
	if err != nil {
		writeLogBuildError(w, r, "build custom log entry", err)
		return
	}
	if err := h.repo.CreateNutritionLogEntry(r.Context(), entry); err != nil {
		httpresp.ServerError(w, r.Context(), "create custom nutrition log entry", err)
		return
	}
	httpresp.Created(w, "logged custom meal", toLogDTO(*entry))
}

// buildCustomLogEntry validates a one-off custom-meal request and returns
// the unsaved NutritionLogEntry (quantity fixed at 1, macros verbatim).
// Caller-facing problems come back as *logBuildError. It does not insert.
func buildCustomLogEntry(userID string, req customLogEntryRequest) (*NutritionLogEntry, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, &logBuildError{http.StatusBadRequest, "name is required"}
	}
	if len(name) > 200 {
		return nil, &logBuildError{http.StatusBadRequest, "name is too long"}
	}
	if req.Calories < 0 || req.Calories > MaxCalories {
		return nil, &logBuildError{http.StatusBadRequest, "calories out of range"}
	}
	for _, m := range []struct {
		name string
		val  float64
	}{
		{"protein_g", req.ProteinG},
		{"fat_g", req.FatG},
		{"carbs_g", req.CarbsG},
	} {
		if m.val < 0 || m.val > MaxMacroGrams {
			return nil, &logBuildError{http.StatusBadRequest, m.name + " out of range"}
		}
	}
	meal := MealType(req.Meal)
	if !meal.Valid() {
		return nil, &logBuildError{http.StatusBadRequest, ErrInvalidMeal.Error()}
	}
	consumedAt := time.Now().UTC()
	if req.ConsumedAt != nil {
		consumedAt = req.ConsumedAt.UTC()
	}
	return &NutritionLogEntry{
		UserID:         userID,
		ConsumedAt:     consumedAt,
		CustomMealName: &name,
		Quantity:       1,
		Calories:       req.Calories,
		ProteinG:       req.ProteinG,
		FatG:           req.FatG,
		CarbsG:         req.CarbsG,
		Meal:           meal,
	}, nil
}

// createLogEntriesBatch logs a heterogeneous list of items best-effort:
// each item is validated and inserted independently, a failure on one
// never aborts the loop or rolls back a sibling, and per-item outcomes are
// returned index-aligned to the request. The envelope is 400 only when it
// is itself malformed (no items, or over the cap); otherwise it is 200
// even if every item failed.
func (h *Handler) createLogEntriesBatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req batchLogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Items) == 0 {
		httpresp.Error(w, http.StatusBadRequest, "items is required")
		return
	}
	if len(req.Items) > MaxBatchLogItems {
		httpresp.Error(w, http.StatusBadRequest, "too many items in one batch")
		return
	}

	results := make([]batchLogResultDTO, 0, len(req.Items))
	logged, failed := 0, 0
	for i, item := range req.Items {
		entry, err := h.buildBatchEntry(r.Context(), userID, item)
		if err == nil {
			err = h.repo.CreateNutritionLogEntry(r.Context(), entry)
		}
		if err != nil {
			failed++
			results = append(results, batchLogResultDTO{
				Index: i,
				OK:    false,
				Error: batchItemErrorMessage(r, err),
			})
			continue
		}
		logged++
		dto := toLogDTO(*entry)
		results = append(results, batchLogResultDTO{Index: i, OK: true, Entry: &dto})
	}
	httpresp.OK(w, "logged consumption batch", batchLogResponseDTO{
		Results: results,
		Logged:  logged,
		Failed:  failed,
	})
}

// buildBatchEntry dispatches one batch item on its kind to the shared
// build helper, mapping the item's fields onto the single endpoints'
// request shapes.
func (h *Handler) buildBatchEntry(ctx context.Context, userID string, item batchLogItemRequest) (*NutritionLogEntry, error) {
	switch item.Kind {
	case "pantry":
		return h.buildLogEntry(ctx, userID, logEntryRequest{
			PantryItemID: item.PantryItemID,
			Quantity:     item.Quantity,
			Meal:         item.Meal,
			ConsumedAt:   item.ConsumedAt,
		})
	case "recipe":
		return h.buildLogEntry(ctx, userID, logEntryRequest{
			RecipeID:   item.RecipeID,
			Quantity:   item.Quantity,
			Meal:       item.Meal,
			ConsumedAt: item.ConsumedAt,
		})
	case "custom":
		return buildCustomLogEntry(userID, customLogEntryRequest{
			Name:       item.Name,
			Calories:   item.Calories,
			ProteinG:   item.ProteinG,
			FatG:       item.FatG,
			CarbsG:     item.CarbsG,
			Meal:       item.Meal,
			ConsumedAt: item.ConsumedAt,
		})
	default:
		return nil, &logBuildError{http.StatusBadRequest, "unknown item kind: " + item.Kind}
	}
}

// batchItemErrorMessage renders a per-item error for the results array.
// Caller-facing build errors carry their own message; an unexpected
// storage failure is logged server-side and reported generically so a raw
// internal error never leaks to the agent.
func batchItemErrorMessage(r *http.Request, err error) string {
	var be *logBuildError
	if errors.As(err, &be) {
		return be.msg
	}
	logServerError(r.Context(), "batch log item insert", err)
	return "internal error"
}

func (h *Handler) listLogEntries(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	start, end, _, err := parseDateRangeQuery(r)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := h.repo.ListNutritionLogEntries(r.Context(), userID, &start, &end)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list nutrition log", err)
		return
	}
	out := make([]logEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, toLogDTO(e))
	}
	httpresp.OK(w, "listed nutrition log", out)
}

func (h *Handler) getLogEntry(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "log entry id is required")
		return
	}
	entry, err := h.repo.GetNutritionLogEntry(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "log entry not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get log entry", err)
		return
	}
	httpresp.OK(w, "fetched log entry", toLogDTO(*entry))
}

func (h *Handler) updateLogEntry(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "log entry id is required")
		return
	}
	var req logEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Look up the existing entry so we can preserve fields the
	// request doesn't touch and re-derive macros against the original
	// pantry item with the new quantity.
	existing, err := h.repo.GetNutritionLogEntry(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "log entry not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get log entry", err)
		return
	}

	// The custom-only fields (name + the four macros) are editable only
	// on a custom-meal row. On a pantry/recipe-backed row those macros
	// derive from the source, so reject the first offending field rather
	// than silently dropping it.
	if existing.CustomMealName == nil {
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"name", req.Name != nil},
			{"calories", req.Calories != nil},
			{"protein_g", req.ProteinG != nil},
			{"fat_g", req.FatG != nil},
			{"carbs_g", req.CarbsG != nil},
		} {
			if f.set {
				httpresp.Error(w, http.StatusBadRequest, f.name+" is only editable on custom meal entries")
				return
			}
		}
	}

	quantity := existing.Quantity
	if req.Quantity > 0 {
		quantity = req.Quantity
	}
	consumedAt := existing.ConsumedAt
	if req.ConsumedAt != nil {
		consumedAt = req.ConsumedAt.UTC()
	}
	// Meal change is optional on update — empty string means "keep
	// the existing meal." Non-empty must validate, since the schema
	// CHECK would reject anything else.
	meal := existing.Meal
	if req.Meal != "" {
		next := MealType(req.Meal)
		if !next.Valid() {
			httpresp.Error(w, http.StatusBadRequest, ErrInvalidMeal.Error())
			return
		}
		meal = next
	}

	// Re-derive macros from whichever source the original entry
	// pointed at: pantry item or recipe. We preserve the reference
	// type — clients can update quantity / time / meal but not
	// switch a pantry-backed entry into a recipe-backed one (which
	// would be closer to creating a new entry anyway).
	entry := &NutritionLogEntry{
		ID:         id,
		UserID:     userID,
		ConsumedAt: consumedAt,
		Quantity:   quantity,
		Meal:       meal,
	}
	switch {
	case existing.CustomMealName != nil:
		// Custom rows have no source to re-derive against — carry the
		// existing name + macros forward and overwrite only the fields
		// the request supplied (each range-checked the same way the
		// POST /custom path validates).
		entry.CustomMealName = existing.CustomMealName
		entry.Calories = existing.Calories
		entry.ProteinG = existing.ProteinG
		entry.FatG = existing.FatG
		entry.CarbsG = existing.CarbsG
		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				httpresp.Error(w, http.StatusBadRequest, "name is required")
				return
			}
			if len(name) > 200 {
				httpresp.Error(w, http.StatusBadRequest, "name is too long")
				return
			}
			entry.CustomMealName = &name
		}
		if req.Calories != nil {
			if *req.Calories < 0 || *req.Calories > MaxCalories {
				httpresp.Error(w, http.StatusBadRequest, "calories out of range")
				return
			}
			entry.Calories = *req.Calories
		}
		for _, m := range []struct {
			name string
			val  *float64
			dst  *float64
		}{
			{"protein_g", req.ProteinG, &entry.ProteinG},
			{"fat_g", req.FatG, &entry.FatG},
			{"carbs_g", req.CarbsG, &entry.CarbsG},
		} {
			if m.val == nil {
				continue
			}
			if *m.val < 0 || *m.val > MaxMacroGrams {
				httpresp.Error(w, http.StatusBadRequest, m.name+" out of range")
				return
			}
			*m.dst = *m.val
		}
	case existing.PantryItemID != nil:
		pantry, err := h.repo.GetPantryItem(r.Context(), userID, *existing.PantryItemID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				httpresp.Error(w, http.StatusConflict, "source pantry item is no longer available")
				return
			}
			httpresp.ServerError(w, r.Context(), "look up pantry item", err)
			return
		}
		entry.PantryItemID = existing.PantryItemID
		entry.Calories = quantity * pantry.Calories
		entry.ProteinG = quantity * pantry.ProteinG
		entry.FatG = quantity * pantry.FatG
		entry.CarbsG = quantity * pantry.CarbsG
	case existing.RecipeID != nil:
		macros, err := h.repo.ComputeRecipeMacros(r.Context(), userID, *existing.RecipeID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				httpresp.Error(w, http.StatusConflict, "source recipe is no longer available")
				return
			}
			httpresp.ServerError(w, r.Context(), "compute recipe macros", err)
			return
		}
		scaled := macros.Scale(quantity)
		entry.RecipeID = existing.RecipeID
		entry.Calories = scaled.Calories
		entry.ProteinG = scaled.ProteinG
		entry.FatG = scaled.FatG
		entry.CarbsG = scaled.CarbsG
	default:
		httpresp.Error(w, http.StatusBadRequest, "log entry has no source to recompute against")
		return
	}
	if err := h.repo.UpdateNutritionLogEntry(r.Context(), entry); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "log entry not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update log entry", err)
		return
	}
	httpresp.OK(w, "updated log entry", toLogDTO(*entry))
}

func (h *Handler) deleteLogEntry(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "log entry id is required")
		return
	}
	if err := h.repo.DeleteNutritionLogEntry(r.Context(), userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "log entry not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete log entry", err)
		return
	}
	httpresp.OK(w, "deleted log entry", nil)
}

func (h *Handler) dailyMacros(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	start, end, loc, err := parseDateRangeQuery(r)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	days, err := h.repo.DailyMacros(r.Context(), userID, start, end, loc)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "daily macros", err)
		return
	}
	out := make([]dailyMacrosDTO, 0, len(days))
	for _, d := range days {
		out = append(out, dailyMacrosDTO(d))
	}
	httpresp.OK(w, "daily macros", out)
}

// --- helpers -------------------------------------------------------

// parseDateRangeQuery resolves the timezone-aware date contract (a required
// IANA timezone plus either date or start_date+end_date) shared by the
// nutrition list and daily-macros endpoints into the UTC half-open interval
// [start, end) that brackets the user-local calendar day(s), plus the resolved
// *time.Location for downstream local-date grouping. The contract itself lives
// in internal/daterange (the planned-workout list reuses it); this stays as the
// package's call site so both handlers read the same way.
func parseDateRangeQuery(r *http.Request) (time.Time, time.Time, *time.Location, error) {
	return daterange.ParseQuery(r.URL.Query())
}

// logBuildError is a caller-facing failure from the log-entry build
// helpers (bad input or a missing/unowned source). It carries the HTTP
// status and message the single handlers already return, so extracting
// the build logic doesn't change their 400/404 responses. Storage/system
// failures are returned as plain errors instead and map to 500.
type logBuildError struct {
	status int
	msg    string
}

func (e *logBuildError) Error() string { return e.msg }

// writeLogBuildError maps a build-helper error onto the response: a
// *logBuildError becomes its carried status+message, anything else is a
// 500 logged under op.
func writeLogBuildError(w http.ResponseWriter, r *http.Request, op string, err error) {
	var be *logBuildError
	if errors.As(err, &be) {
		httpresp.Error(w, be.status, be.msg)
		return
	}
	httpresp.ServerError(w, r.Context(), op, err)
}

// logServerError records an unexpected server-side failure without writing
// a response. It mirrors how httpresp.ServerError logs (op + err) for the
// best-effort batch path, where a per-item storage failure must be recorded
// but the envelope still returns 200. ctx is accepted for parity with
// httpresp.ServerError (reserved for structured logging).
func logServerError(ctx context.Context, op string, err error) {
	_ = ctx
	log.Printf("%s: %v", op, err)
}

// isValidationError reports whether err is one of the package's
// caller-facing validation sentinels (vs. a storage / system error).
// Lets handlers map domain validation to 400 without enumerating
// every error in every handler.
func isValidationError(err error) bool {
	switch {
	case errors.Is(err, ErrNameRequired),
		errors.Is(err, ErrServingUnitRequired),
		errors.Is(err, ErrMacrosNegative),
		errors.Is(err, ErrServingSizeNonPositive),
		errors.Is(err, ErrQuantityNonPositive),
		errors.Is(err, ErrLogEntryReferenceRequired),
		errors.Is(err, ErrInvalidMeal):
		return true
	}
	return false
}
