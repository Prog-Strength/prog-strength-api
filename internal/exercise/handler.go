package exercise

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler exposes HTTP endpoints for the exercise catalog.
type Handler struct {
	repo Repository
}

// NewHandler builds a Handler backed by the given repository.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// Mount registers exercise routes on the given router.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/exercises", func(r chi.Router) {
		r.Get("/", h.list)
		r.Get("/{id}", h.get)
	})
}

// list handles GET /exercises with optional filters:
//
//	?muscle_group=quads
//	?equipment=barbell
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := ListOptions{
		MuscleGroup: MuscleGroup(q.Get("muscle_group")),
		Equipment:   Equipment(q.Get("equipment")),
	}

	if opts.MuscleGroup != "" && !opts.MuscleGroup.Valid() {
		httpresp.Error(w, http.StatusBadRequest, "invalid muscle_group")
		return
	}
	if opts.Equipment != "" && !opts.Equipment.Valid() {
		httpresp.Error(w, http.StatusBadRequest, "invalid equipment")
		return
	}

	exercises, err := h.repo.List(r.Context(), opts)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list exercises", err)
		return
	}

	httpresp.OK(w, "retrieved exercises", exercises)
}

// get handles GET /exercises/{id}.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httpresp.Error(w, http.StatusBadRequest, "id is required")
		return
	}

	ex, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "exercise not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get exercise", err)
		return
	}

	httpresp.OK(w, "retrieved exercise", ex)
}
