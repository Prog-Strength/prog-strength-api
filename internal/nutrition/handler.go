package nutrition

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
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
		r.Get("/", h.listLogEntries)
		r.Post("/", h.createLogEntry)
		r.Get("/{id}", h.getLogEntry)
		r.Put("/{id}", h.updateLogEntry)
		r.Delete("/{id}", h.deleteLogEntry)
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
	Quantity     float64   `json:"quantity"`
	Calories     float64   `json:"calories"`
	ProteinG     float64   `json:"protein_g"`
	FatG         float64   `json:"fat_g"`
	CarbsG       float64   `json:"carbs_g"`
	CreatedAt    time.Time `json:"created_at"`
}

func toLogDTO(e NutritionLogEntry) logEntryDTO {
	return logEntryDTO{
		ID:           e.ID,
		ConsumedAt:   e.ConsumedAt,
		PantryItemID: e.PantryItemID,
		RecipeID:     e.RecipeID,
		Quantity:     e.Quantity,
		Calories:     e.Calories,
		ProteinG:     e.ProteinG,
		FatG:         e.FatG,
		CarbsG:       e.CarbsG,
		CreatedAt:    e.CreatedAt,
	}
}

type logEntryRequest struct {
	ConsumedAt   *time.Time `json:"consumed_at"`
	PantryItemID *string    `json:"pantry_item_id"`
	RecipeID     *string    `json:"recipe_id"`
	Quantity     float64    `json:"quantity"`
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
	// Phase 1: recipes not yet supported. Reject up front so the
	// failure mode is clear rather than a downstream FK error.
	if req.RecipeID != nil && *req.RecipeID != "" {
		httpresp.Error(w, http.StatusBadRequest, "recipe-based log entries are not yet supported")
		return
	}
	if req.PantryItemID == nil || *req.PantryItemID == "" {
		httpresp.Error(w, http.StatusBadRequest, "pantry_item_id is required")
		return
	}
	if req.Quantity <= 0 {
		httpresp.Error(w, http.StatusBadRequest, ErrQuantityNonPositive.Error())
		return
	}

	// Look up the pantry item to derive denormalized macros at log time.
	// Ownership is enforced inside GetPantryItem; a cross-user reference
	// returns ErrNotFound (which we surface as 404, not 403, deliberately).
	pantry, err := h.repo.GetPantryItem(r.Context(), userID, *req.PantryItemID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "pantry item not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "look up pantry item", err)
		return
	}

	consumedAt := time.Now().UTC()
	if req.ConsumedAt != nil {
		consumedAt = req.ConsumedAt.UTC()
	}
	entry := &NutritionLogEntry{
		UserID:       userID,
		ConsumedAt:   consumedAt,
		PantryItemID: req.PantryItemID,
		Quantity:     req.Quantity,
		Calories:     req.Quantity * pantry.Calories,
		ProteinG:     req.Quantity * pantry.ProteinG,
		FatG:         req.Quantity * pantry.FatG,
		CarbsG:       req.Quantity * pantry.CarbsG,
	}
	if err := h.repo.CreateNutritionLogEntry(r.Context(), entry); err != nil {
		httpresp.ServerError(w, r.Context(), "create nutrition log entry", err)
		return
	}
	httpresp.Created(w, "logged consumption", toLogDTO(*entry))
}

func (h *Handler) listLogEntries(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	since, until, err := parseSinceUntil(r)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := h.repo.ListNutritionLogEntries(r.Context(), userID, since, until)
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

	quantity := existing.Quantity
	if req.Quantity > 0 {
		quantity = req.Quantity
	}
	consumedAt := existing.ConsumedAt
	if req.ConsumedAt != nil {
		consumedAt = req.ConsumedAt.UTC()
	}

	// Phase 1 entries always have PantryItemID set; recompute macros
	// from the source item. If the source pantry item has been
	// soft-deleted, GetPantryItem returns ErrNotFound and we surface
	// a 409 so the caller knows the issue is data state, not the
	// request shape.
	if existing.PantryItemID == nil {
		httpresp.Error(w, http.StatusBadRequest, "log entry has no pantry item to recompute against")
		return
	}
	pantry, err := h.repo.GetPantryItem(r.Context(), userID, *existing.PantryItemID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusConflict, "source pantry item is no longer available")
			return
		}
		httpresp.ServerError(w, r.Context(), "look up pantry item", err)
		return
	}

	entry := &NutritionLogEntry{
		ID:           id,
		UserID:       userID,
		ConsumedAt:   consumedAt,
		PantryItemID: existing.PantryItemID,
		Quantity:     quantity,
		Calories:     quantity * pantry.Calories,
		ProteinG:     quantity * pantry.ProteinG,
		FatG:         quantity * pantry.FatG,
		CarbsG:       quantity * pantry.CarbsG,
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
	sinceRaw := r.URL.Query().Get("since")
	untilRaw := r.URL.Query().Get("until")
	if sinceRaw == "" || untilRaw == "" {
		httpresp.Error(w, http.StatusBadRequest, "since and until are required (RFC3339)")
		return
	}
	since, err := time.Parse(time.RFC3339, sinceRaw)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid since (expected RFC3339)")
		return
	}
	until, err := time.Parse(time.RFC3339, untilRaw)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid until (expected RFC3339)")
		return
	}
	if !since.Before(until) {
		httpresp.Error(w, http.StatusBadRequest, "since must be strictly before until")
		return
	}

	days, err := h.repo.DailyMacros(r.Context(), userID, since, until)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "daily macros", err)
		return
	}
	out := make([]dailyMacrosDTO, 0, len(days))
	for _, d := range days {
		out = append(out, dailyMacrosDTO{
			Date:       d.Date,
			Calories:   d.Calories,
			ProteinG:   d.ProteinG,
			FatG:       d.FatG,
			CarbsG:     d.CarbsG,
			EntryCount: d.EntryCount,
		})
	}
	httpresp.OK(w, "daily macros", out)
}

// --- helpers -------------------------------------------------------

// parseSinceUntil reads optional since/until query params as RFC3339.
// Returns (nil, nil, nil) when neither is set — the caller treats
// that as "no filter."
func parseSinceUntil(r *http.Request) (*time.Time, *time.Time, error) {
	var since, until *time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, errors.New("invalid since (expected RFC3339)")
		}
		since = &t
	}
	if raw := r.URL.Query().Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, errors.New("invalid until (expected RFC3339)")
		}
		until = &t
	}
	if since != nil && until != nil && !since.Before(*until) {
		return nil, nil, errors.New("since must be strictly before until")
	}
	return since, until, nil
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
		errors.Is(err, ErrLogEntryReferenceRequired):
		return true
	}
	return false
}
