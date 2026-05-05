package exercise

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
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
		writeError(w, http.StatusBadRequest, "invalid muscle_group")
		return
	}
	if opts.Equipment != "" && !opts.Equipment.Valid() {
		writeError(w, http.StatusBadRequest, "invalid equipment")
		return
	}

	exercises, err := h.repo.List(r.Context(), opts)
	if err != nil {
		writeServerError(w, r.Context(), "list exercises", err)
		return
	}

	writeJSON(w, http.StatusOK, listResponse{Data: exercises})
}

// get handles GET /exercises/{id}.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	ex, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "exercise not found")
			return
		}
		writeServerError(w, r.Context(), "get exercise", err)
		return
	}

	writeJSON(w, http.StatusOK, ex)
}

// listResponse wraps list endpoints in an envelope so future fields
// (pagination cursors, totals, etc.) can be added without breaking clients.
type listResponse struct {
	Data []Exercise `json:"data"`
}

// errorResponse is the standard error envelope returned by all handlers.
type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Headers are already sent; log and move on.
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeServerError(w http.ResponseWriter, ctx context.Context, op string, err error) {
	log.Printf("%s: %v", op, err)
	writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
}
