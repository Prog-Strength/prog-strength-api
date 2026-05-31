package nutrition

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// macroGoalsDTO is the wire shape for GET /me/macro-goals and the
// response from PUT. created_at / updated_at are pointers so the
// "never set" case (zero-valued row from the repo) serializes as
// null timestamps the client can detect to render the empty state.
type macroGoalsDTO struct {
	ProteinG  int        `json:"protein_g"`
	CarbsG    int        `json:"carbs_g"`
	FatG      int        `json:"fat_g"`
	Calories  int        `json:"calories"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func toMacroGoalsDTO(g MacroGoals) macroGoalsDTO {
	return macroGoalsDTO{
		ProteinG:  g.ProteinG,
		CarbsG:    g.CarbsG,
		FatG:      g.FatG,
		Calories:  g.Calories,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
	}
}

// putMacroGoalsRequest mirrors the persisted shape minus timestamps.
// We use *int instead of int so a missing field decodes to nil and we
// can reject it with a specific "X is required" message — Go's
// zero-value int would otherwise be indistinguishable from an explicit
// 0 (which is a legitimate value here: "clear my protein target").
type putMacroGoalsRequest struct {
	ProteinG *int `json:"protein_g"`
	CarbsG   *int `json:"carbs_g"`
	FatG     *int `json:"fat_g"`
	Calories *int `json:"calories"`
}

// getMyMacroGoals handles GET /me/macro-goals.
//
// Always returns 200. When the user has never set goals, the repo
// returns a zero-valued struct with nil timestamps; the JSON response
// surfaces those as null, and the client uses that as the "render the
// empty-state ring outline" signal — no 404 dance.
func (h *Handler) getMyMacroGoals(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	g, err := h.repo.GetMacroGoals(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get macro goals", err)
		return
	}
	httpresp.OK(w, "fetched macro goals", toMacroGoalsDTO(g))
}

// putMyMacroGoals handles PUT /me/macro-goals.
//
// Set-replacement semantics: all four fields are required (the four
// numbers are conceptually one goal, not four independent ones), and
// every persisted goal carries values for all four. Validation
// matches the SQL CHECK constraints in 009_user_macro_goals.sql so
// the layers can't disagree on what's accepted.
func (h *Handler) putMyMacroGoals(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req putMacroGoalsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := validateMacroGoalsRequest(req); msg != "" {
		httpresp.Error(w, http.StatusBadRequest, msg)
		return
	}
	saved, err := h.repo.UpsertMacroGoals(r.Context(), MacroGoals{
		UserID:   userID,
		ProteinG: *req.ProteinG,
		CarbsG:   *req.CarbsG,
		FatG:     *req.FatG,
		Calories: *req.Calories,
	}, time.Now().UTC())
	if err != nil {
		httpresp.ServerError(w, r.Context(), "upsert macro goals", err)
		return
	}
	httpresp.OK(w, "saved macro goals", toMacroGoalsDTO(saved))
}

// validateMacroGoalsRequest checks all four fields are present and
// within the per-macro / calorie caps. Returns "" on success and an
// HTTP-400-ready message on the first failure (first-error-wins, per
// the CLAUDE.md convention).
func validateMacroGoalsRequest(req putMacroGoalsRequest) string {
	type field struct {
		name string
		val  *int
		max  int
	}
	checks := []field{
		{"protein_g", req.ProteinG, MaxMacroGrams},
		{"carbs_g", req.CarbsG, MaxMacroGrams},
		{"fat_g", req.FatG, MaxMacroGrams},
		{"calories", req.Calories, MaxCalories},
	}
	for _, c := range checks {
		if c.val == nil {
			return fmt.Sprintf("%s is required", c.name)
		}
		if *c.val < 0 {
			return fmt.Sprintf("%s must be non-negative", c.name)
		}
		if *c.val > c.max {
			return fmt.Sprintf("%s must be ≤ %d", c.name, c.max)
		}
	}
	return ""
}
