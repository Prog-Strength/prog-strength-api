package user

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler serves the authed user's own account at /me. GET reads the account;
// PATCH is the preferences write path. The frontend needs the user object
// (notably weight_unit and distance_unit) to render user-scoped views without
// threading preferences through every request.
type Handler struct {
	repo Repository
}

func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// Mount registers routes on the given router. The router is expected to be
// the JWT-gated group (wrapped in auth.RequireUser) — getMe reads the user
// ID out of request context, which the middleware populates.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/me", h.getMe)
	r.Patch("/me", h.updateMe)
}

// updateMeRequest is the PATCH /me body. Fields are pointers so absence
// (nil) is distinguishable from a zero value — only provided fields are
// applied, making the update additive/partial.
type updateMeRequest struct {
	DisplayName  *string       `json:"display_name"`
	WeightUnit   *WeightUnit   `json:"weight_unit"`
	DistanceUnit *DistanceUnit `json:"distance_unit"`
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	u, err := h.repo.GetByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get user", err)
		return
	}

	// Return *User directly: its JSON tags produce the right shape and
	// hide DeletedAt (json:"-"), so no DTO is needed.
	httpresp.OK(w, "got user", u)
}

// updateMe is the preferences write path. It loads the current user, applies
// only the fields present in the request body (additive/partial update),
// validates, and persists. Email is immutable through this path (handled by
// the repository), so it isn't exposed in the request body.
func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	var req updateMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	u, err := h.repo.GetByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get user", err)
		return
	}

	// Apply only provided fields, leaving the rest untouched.
	if req.DisplayName != nil {
		u.DisplayName = *req.DisplayName
	}
	if req.WeightUnit != nil {
		u.WeightUnit = *req.WeightUnit
	}
	if req.DistanceUnit != nil {
		u.DistanceUnit = *req.DistanceUnit
	}

	// Validate at the boundary: a blank display name or an unknown enum is
	// a client error, not a server error.
	if err := u.Validate(); err != nil {
		var enumErr *InvalidEnumError
		if errors.Is(err, ErrDisplayNameRequired) || errors.As(err, &enumErr) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "validate user", err)
		return
	}

	if err := h.repo.Update(r.Context(), u); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "user not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update user", err)
		return
	}

	httpresp.OK(w, "updated user", u)
}
