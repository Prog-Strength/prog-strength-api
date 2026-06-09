package nutrition

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// --- DTOs ----------------------------------------------------------

// recipeDTO is the API shape for a recipe. Components carry the
// pantry item ID + quantity + position (display order); derived
// macros sit on the recipe itself so the frontend doesn't have to
// re-aggregate per render.
type recipeDTO struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Components []recipeItemDTO `json:"components"`
	Macros     recipeMacrosDTO `json:"macros"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

type recipeItemDTO struct {
	ID           string  `json:"id"`
	PantryItemID string  `json:"pantry_item_id"`
	Quantity     float64 `json:"quantity"`
	Position     int     `json:"position"`
}

type recipeMacrosDTO struct {
	Calories float64 `json:"calories"`
	ProteinG float64 `json:"protein_g"`
	FatG     float64 `json:"fat_g"`
	CarbsG   float64 `json:"carbs_g"`
}

// recipeRequest is the body for both POST /recipes and PUT /recipes/{id}.
type recipeRequest struct {
	Name       string                 `json:"name"`
	Components []recipeComponentInput `json:"components"`
}

type recipeComponentInput struct {
	PantryItemID string  `json:"pantry_item_id"`
	Quantity     float64 `json:"quantity"`
}

// --- Recipe handlers -----------------------------------------------

func (h *Handler) createRecipe(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req recipeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRecipeComponents(r, userID, req.Components); err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	recipe := buildRecipeFromRequest(userID, "", req)
	if err := h.repo.CreateRecipe(r.Context(), recipe); err != nil {
		if isRecipeValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "create recipe", err)
		return
	}
	dto, err := h.toRecipeDTO(r, userID, recipe)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build recipe response", err)
		return
	}
	httpresp.Created(w, "created recipe", dto)
}

func (h *Handler) listRecipes(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	recipes, err := h.repo.ListRecipes(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list recipes", err)
		return
	}
	out := make([]recipeDTO, 0, len(recipes))
	for i := range recipes {
		dto, err := h.toRecipeDTO(r, userID, &recipes[i])
		if err != nil {
			httpresp.ServerError(w, r.Context(), "build recipe response", err)
			return
		}
		out = append(out, dto)
	}
	httpresp.OK(w, "listed recipes", out)
}

func (h *Handler) getRecipe(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "recipe id is required")
		return
	}
	recipe, err := h.repo.GetRecipe(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "recipe not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get recipe", err)
		return
	}
	dto, err := h.toRecipeDTO(r, userID, recipe)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build recipe response", err)
		return
	}
	httpresp.OK(w, "fetched recipe", dto)
}

func (h *Handler) updateRecipe(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "recipe id is required")
		return
	}
	var req recipeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateRecipeComponents(r, userID, req.Components); err != nil {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	recipe := buildRecipeFromRequest(userID, id, req)
	if err := h.repo.UpdateRecipe(r.Context(), recipe); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "recipe not found")
			return
		}
		if isRecipeValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "update recipe", err)
		return
	}
	dto, err := h.toRecipeDTO(r, userID, recipe)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "build recipe response", err)
		return
	}
	httpresp.OK(w, "updated recipe", dto)
}

func (h *Handler) deleteRecipe(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "recipe id is required")
		return
	}
	if err := h.repo.DeleteRecipe(r.Context(), userID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "recipe not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete recipe", err)
		return
	}
	httpresp.OK(w, "deleted recipe", nil)
}

// --- helpers -------------------------------------------------------

// validateRecipeComponents confirms every component's pantry item
// exists, belongs to the user, isn't soft-deleted, and has a positive
// quantity. Shape-only checks (non-empty list, cap, duplicates) run
// inside the repo's Validate; we run them here too so the handler
// gets the right error class without trying the write first.
func (h *Handler) validateRecipeComponents(r *http.Request, userID string, comps []recipeComponentInput) error {
	if len(comps) == 0 {
		return ErrRecipeComponentsRequired
	}
	if len(comps) > MaxRecipeComponents {
		return fmt.Errorf("at most %d components allowed in a recipe", MaxRecipeComponents)
	}
	seen := make(map[string]bool, len(comps))
	for _, c := range comps {
		if c.PantryItemID == "" {
			return ErrRecipeComponentPantryRequired
		}
		if seen[c.PantryItemID] {
			return fmt.Errorf("duplicate component: %s", c.PantryItemID)
		}
		seen[c.PantryItemID] = true
		if c.Quantity <= 0 {
			return ErrQuantityNonPositive
		}
		// Confirm the pantry item exists + belongs to the user.
		// GetPantryItem hides soft-deleted rows; a recipe builder
		// can't add a deleted item to a new recipe.
		if _, err := h.repo.GetPantryItem(r.Context(), userID, c.PantryItemID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("unknown pantry item: %s", c.PantryItemID)
			}
			return err
		}
	}
	return nil
}

func buildRecipeFromRequest(userID, id string, req recipeRequest) *Recipe {
	components := make([]RecipeItem, 0, len(req.Components))
	for _, c := range req.Components {
		components = append(components, RecipeItem{
			PantryItemID: c.PantryItemID,
			Quantity:     c.Quantity,
		})
	}
	return &Recipe{
		ID:         id,
		UserID:     userID,
		Name:       req.Name,
		Components: components,
	}
}

func (h *Handler) toRecipeDTO(r *http.Request, userID string, rec *Recipe) (recipeDTO, error) {
	macros, err := h.repo.ComputeRecipeMacros(r.Context(), userID, rec.ID)
	if err != nil {
		return recipeDTO{}, err
	}
	out := recipeDTO{
		ID:        rec.ID,
		Name:      rec.Name,
		CreatedAt: rec.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: rec.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Macros: recipeMacrosDTO{
			Calories: macros.Calories,
			ProteinG: macros.ProteinG,
			FatG:     macros.FatG,
			CarbsG:   macros.CarbsG,
		},
		Components: make([]recipeItemDTO, 0, len(rec.Components)),
	}
	for _, c := range rec.Components {
		out.Components = append(out.Components, recipeItemDTO{
			ID:           c.ID,
			PantryItemID: c.PantryItemID,
			Quantity:     c.Quantity,
			Position:     c.Position,
		})
	}
	return out, nil
}

func isRecipeValidationError(err error) bool {
	switch {
	case errors.Is(err, ErrNameRequired),
		errors.Is(err, ErrRecipeComponentsRequired),
		errors.Is(err, ErrRecipeTooManyComponents),
		errors.Is(err, ErrRecipeComponentPantryRequired),
		errors.Is(err, ErrRecipeComponentDuplicate),
		errors.Is(err, ErrQuantityNonPositive):
		return true
	}
	return false
}
