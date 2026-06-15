package steps

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// goalDTO is the wire shape for GET /me/steps-goal and the response from
// PUT. created_at / updated_at are pointers so the "never set" case (a
// zero-valued Goal from the repo) serializes as null timestamps the client
// can detect to render the empty state.
type goalDTO struct {
	Goal      int        `json:"goal"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// toGoalDTO maps a Goal to its wire shape. The repo returns a zero-valued
// Goal (Goal == 0) for the never-set state, which surfaces directly as
// {"goal": 0, "created_at": null, "updated_at": null}.
func toGoalDTO(g Goal) goalDTO {
	return goalDTO{
		Goal:      g.Goal,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
	}
}

// putStepsGoalRequest mirrors the persisted shape minus timestamps. The
// pointer lets us tell a missing field from an explicit zero so we can
// reject each with a specific message.
type putStepsGoalRequest struct {
	Goal *int `json:"goal"`
}

// getMyStepsGoal handles GET /me/steps-goal.
//
// Always returns 200. When the user has never set a goal the repo returns
// a zero-valued Goal with nil timestamps; the JSON response surfaces those
// as {goal: 0, ...null}, and the client uses that as the "render the
// empty-state" signal — no 404 dance.
func (h *Handler) getMyStepsGoal(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	g, err := h.repo.GetGoal(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get steps goal", err)
		return
	}
	httpresp.OK(w, "fetched steps goal", toGoalDTO(g))
}

// putMyStepsGoal handles PUT /me/steps-goal.
//
// Set-replacement semantics: goal is required and replaces any existing
// one. Validation matches Goal.Validate / the SQL CHECK so the layers can't
// disagree on what's accepted.
func (h *Handler) putMyStepsGoal(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req putStepsGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Goal == nil || *req.Goal <= 0 || *req.Goal > MaxGoal {
		httpresp.Error(w, http.StatusBadRequest, ErrGoalOutOfRange.Error())
		return
	}
	saved, err := h.repo.UpsertGoal(r.Context(), Goal{
		UserID: userID,
		Goal:   *req.Goal,
	}, time.Now().UTC())
	if err != nil {
		httpresp.ServerError(w, r.Context(), "upsert steps goal", err)
		return
	}
	httpresp.OK(w, "saved steps goal", toGoalDTO(saved))
}
