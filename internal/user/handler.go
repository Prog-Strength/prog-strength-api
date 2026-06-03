package user

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler serves the authed user's own account at /me. Read-only for now:
// the frontend needs the user object (notably weight_unit) to render
// user-scoped views without threading preferences through every request.
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
