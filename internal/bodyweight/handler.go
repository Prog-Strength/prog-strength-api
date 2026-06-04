package bodyweight

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Handler serves /bodyweight: POST creates, GET lists, PUT edits, and
// DELETE soft-deletes a reading. PUT overlays only the supplied fields
// onto the existing row so corrections stay scoped to what changed.
type Handler struct {
	repo     Repository
	userRepo user.Repository
}

func NewHandler(repo Repository, userRepo user.Repository) *Handler {
	return &Handler{repo: repo, userRepo: userRepo}
}

// Mount registers routes on the given router. Callers are expected to
// have already wrapped the router in auth.RequireUser — these handlers
// read the user ID out of request context.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/bodyweight", func(r chi.Router) {
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Put("/{id}", h.update)
		r.Delete("/{id}", h.delete)
	})
	// Per-user bodyweight goal. Lives on /me/... to match the
	// macro-goals convention (one row per user, no listing,
	// set-replacement semantics).
	r.Route("/me/bodyweight-goal", func(r chi.Router) {
		r.Get("/", h.getMyBodyweightGoal)
		r.Put("/", h.putMyBodyweightGoal)
	})
}

// --- DTOs ----------------------------------------------------------

type entryDTO struct {
	ID         string    `json:"id"`
	Weight     float64   `json:"weight"`
	Unit       string    `json:"unit"`
	MeasuredAt time.Time `json:"measured_at"`
	CreatedAt  time.Time `json:"created_at"`
}

func toDTO(e Entry) entryDTO {
	return entryDTO{
		ID:         e.ID,
		Weight:     e.Weight,
		Unit:       string(e.Unit),
		MeasuredAt: e.MeasuredAt,
		CreatedAt:  e.CreatedAt,
	}
}

type createRequest struct {
	Weight     float64    `json:"weight"`
	Unit       string     `json:"unit"`
	MeasuredAt *time.Time `json:"measured_at"`
}

// --- Handlers ------------------------------------------------------

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Weight <= 0 {
		httpresp.Error(w, http.StatusBadRequest, ErrWeightNonPositive.Error())
		return
	}

	unit := user.WeightUnit(req.Unit)
	if unit == "" {
		// Per the SOW: unit defaults to the user's preferred unit
		// when the client omits it. We do the lookup here rather
		// than push it onto every caller (frontend or MCP).
		u, err := h.userRepo.GetByID(r.Context(), userID)
		if err == nil {
			unit = u.WeightUnit
		} else {
			// User row should always exist for an authed request, but
			// fall back to lb so the request doesn't 500 on a transient
			// repository error. Logged via ServerError elsewhere; here
			// we just keep the request alive.
			unit = user.WeightUnitPounds
		}
	}
	if !unit.Valid() {
		httpresp.Error(w, http.StatusBadRequest, ErrInvalidUnit.Error())
		return
	}

	measuredAt := time.Now().UTC()
	if req.MeasuredAt != nil {
		measuredAt = req.MeasuredAt.UTC()
	}

	entry := &Entry{
		UserID:     userID,
		Weight:     req.Weight,
		Unit:       unit,
		MeasuredAt: measuredAt,
	}
	if err := h.repo.Create(r.Context(), entry); err != nil {
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "create bodyweight entry", err)
		return
	}
	httpresp.Created(w, "logged bodyweight", toDTO(*entry))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
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
	entries, err := h.repo.List(r.Context(), userID, since, until)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list bodyweight", err)
		return
	}
	out := make([]entryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, toDTO(e))
	}
	httpresp.OK(w, "listed bodyweight entries", out)
}

type updateRequest struct {
	Weight     *float64   `json:"weight"`
	Unit       *string    `json:"unit"`
	MeasuredAt *time.Time `json:"measured_at"`
}

// update handles PUT /bodyweight/{id}. Partial update: look up the
// existing reading, overlay only the fields the request supplied, then
// persist. Validation of supplied fields produces clean 400s; ID,
// UserID, and CreatedAt are always preserved from the existing row.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	entryID := chi.URLParam(r, "id")
	if entryID == "" {
		httpresp.Error(w, http.StatusBadRequest, "bodyweight entry id is required")
		return
	}
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	existing, err := h.repo.Get(r.Context(), userID, entryID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "bodyweight entry not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get bodyweight entry", err)
		return
	}

	updated := *existing
	if req.Weight != nil {
		if *req.Weight <= 0 {
			httpresp.Error(w, http.StatusBadRequest, "weight must be positive")
			return
		}
		updated.Weight = *req.Weight
	}
	if req.Unit != nil {
		if !user.WeightUnit(*req.Unit).Valid() {
			httpresp.Error(w, http.StatusBadRequest, "unit must be 'lb' or 'kg'")
			return
		}
		updated.Unit = user.WeightUnit(*req.Unit)
	}
	if req.MeasuredAt != nil {
		updated.MeasuredAt = req.MeasuredAt.UTC()
	}

	if err := h.repo.UpdateEntry(r.Context(), &updated); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "bodyweight entry not found")
			return
		}
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "update bodyweight entry", err)
		return
	}
	httpresp.OK(w, "updated bodyweight entry", toDTO(updated))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	entryID := chi.URLParam(r, "id")
	if entryID == "" {
		httpresp.Error(w, http.StatusBadRequest, "bodyweight entry id is required")
		return
	}
	if err := h.repo.Delete(r.Context(), userID, entryID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "bodyweight entry not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete bodyweight entry", err)
		return
	}
	httpresp.OK(w, "deleted bodyweight entry", nil)
}

// --- helpers -------------------------------------------------------

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

func isValidationError(err error) bool {
	switch {
	case errors.Is(err, ErrWeightNonPositive),
		errors.Is(err, ErrInvalidUnit),
		errors.Is(err, ErrMeasuredAtRequired):
		return true
	}
	return false
}
