package bodyweight

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// goalDTO is the wire shape for GET /me/bodyweight-goal and the response
// from PUT. created_at / updated_at are pointers so the "never set" case
// (zero-valued Goal from the repo) serializes as null timestamps the
// client can detect to render the empty state.
type goalDTO struct {
	Weight    float64    `json:"weight"`
	Unit      string     `json:"unit"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// toGoalDTO maps a Goal to its wire shape. The repo returns a zero-valued
// Goal (empty Unit) for the never-set state; we surface that as "lb" so
// the client always gets a valid unit to render the empty-state affordance.
func toGoalDTO(g Goal) goalDTO {
	unit := string(g.Unit)
	if unit == "" {
		unit = string(user.WeightUnitPounds)
	}
	return goalDTO{
		Weight:    g.Weight,
		Unit:      unit,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
	}
}

// putBodyweightGoalRequest mirrors the persisted shape minus timestamps.
// Pointers let us tell a missing field from an explicit zero so we can
// reject each with a specific message.
type putBodyweightGoalRequest struct {
	Weight *float64 `json:"weight"`
	Unit   *string  `json:"unit"`
}

// getMyBodyweightGoal handles GET /me/bodyweight-goal.
//
// Always returns 200. When the user has never set a goal the repo returns
// a zero-valued Goal with nil timestamps; the JSON response surfaces those
// as null, and the client uses that as the "render the empty-state" signal
// — no 404 dance.
func (h *Handler) getMyBodyweightGoal(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	g, err := h.repo.GetBodyweightGoal(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get bodyweight goal", err)
		return
	}
	httpresp.OK(w, "fetched bodyweight goal", toGoalDTO(g))
}

// putMyBodyweightGoal handles PUT /me/bodyweight-goal.
//
// Set-replacement semantics: weight + unit are both required and replace
// any existing goal. Validation matches Goal.Validate / the SQL CHECKs so
// the layers can't disagree on what's accepted. First-error-wins.
func (h *Handler) putMyBodyweightGoal(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req putBodyweightGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Weight == nil || *req.Weight <= 0 {
		httpresp.Error(w, http.StatusBadRequest, "weight must be positive")
		return
	}
	if *req.Weight > MaxGoalWeight {
		httpresp.Error(w, http.StatusBadRequest, "weight must be <= 2000")
		return
	}
	if req.Unit == nil || !user.WeightUnit(*req.Unit).Valid() {
		httpresp.Error(w, http.StatusBadRequest, "unit must be 'lb' or 'kg'")
		return
	}
	saved, err := h.repo.UpsertBodyweightGoal(r.Context(), Goal{
		UserID: userID,
		Weight: *req.Weight,
		Unit:   user.WeightUnit(*req.Unit),
	}, time.Now().UTC())
	if err != nil {
		httpresp.ServerError(w, r.Context(), "upsert bodyweight goal", err)
		return
	}
	httpresp.OK(w, "saved bodyweight goal", toGoalDTO(saved))
}
