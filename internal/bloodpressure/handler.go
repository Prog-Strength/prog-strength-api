package bloodpressure

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler serves /blood-pressure: POST creates, GET lists, PUT edits, and
// DELETE soft-deletes a reading. PUT overlays only the supplied fields
// onto the existing row so corrections stay scoped to what changed.
//
// Unlike bodyweight there is no userRepo dependency and no goal routes:
// the healthy-range classification is a code constant (see Classify),
// not per-user state, so nothing needs a user lookup.
type Handler struct {
	repo Repository
}

func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// Mount registers routes on the given router. Callers are expected to
// have already wrapped the router in auth.RequireUser — these handlers
// read the user ID out of request context.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/blood-pressure", func(r chi.Router) {
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Put("/{id}", h.update)
		r.Delete("/{id}", h.delete)
	})
}

// --- DTOs ----------------------------------------------------------

type entryDTO struct {
	ID         string    `json:"id"`
	Systolic   int       `json:"systolic"`
	Diastolic  int       `json:"diastolic"`
	Pulse      *int      `json:"pulse"`
	Category   Category  `json:"category"`
	MeasuredAt time.Time `json:"measured_at"`
	CreatedAt  time.Time `json:"created_at"`
}

func toDTO(e Entry) entryDTO {
	return entryDTO{
		ID:         e.ID,
		Systolic:   e.Systolic,
		Diastolic:  e.Diastolic,
		Pulse:      e.Pulse,
		Category:   e.Category(),
		MeasuredAt: e.MeasuredAt,
		CreatedAt:  e.CreatedAt,
	}
}

type createRequest struct {
	Systolic   int        `json:"systolic"`
	Diastolic  int        `json:"diastolic"`
	Pulse      *int       `json:"pulse"`
	MeasuredAt *time.Time `json:"measured_at"`
}

type updateRequest struct {
	Systolic   *int       `json:"systolic"`
	Diastolic  *int       `json:"diastolic"`
	Pulse      *int       `json:"pulse"`
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

	measuredAt := time.Now().UTC()
	if req.MeasuredAt != nil {
		measuredAt = req.MeasuredAt.UTC()
	}

	entry := &Entry{
		UserID:     userID,
		Systolic:   req.Systolic,
		Diastolic:  req.Diastolic,
		Pulse:      req.Pulse,
		MeasuredAt: measuredAt,
	}
	if err := h.repo.Create(r.Context(), entry); err != nil {
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "create blood pressure entry", err)
		return
	}
	httpresp.Created(w, "logged blood pressure", toDTO(*entry))
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
		httpresp.ServerError(w, r.Context(), "list blood pressure", err)
		return
	}
	out := make([]entryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, toDTO(e))
	}
	httpresp.OK(w, "listed blood pressure readings", out)
}

// update handles PUT /blood-pressure/{id}. Partial update: look up the
// existing reading, overlay only the fields the request supplied, then
// persist. Supplied values are range-checked inline for a clean 400; the
// combined systolic > diastolic invariant is caught by repo validation
// (isValidationError) so changing only one of the pair is still verified.
// ID, UserID, and CreatedAt are always preserved from the existing row.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	entryID := chi.URLParam(r, "id")
	if entryID == "" {
		httpresp.Error(w, http.StatusBadRequest, "blood pressure reading id is required")
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
			httpresp.Error(w, http.StatusNotFound, "blood pressure reading not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get blood pressure reading", err)
		return
	}

	updated := *existing
	if req.Systolic != nil {
		if *req.Systolic < 50 || *req.Systolic > 300 {
			httpresp.Error(w, http.StatusBadRequest, ErrSystolicRange.Error())
			return
		}
		updated.Systolic = *req.Systolic
	}
	if req.Diastolic != nil {
		if *req.Diastolic < 30 || *req.Diastolic > 200 {
			httpresp.Error(w, http.StatusBadRequest, ErrDiastolicRange.Error())
			return
		}
		updated.Diastolic = *req.Diastolic
	}
	if req.Pulse != nil {
		if *req.Pulse < 20 || *req.Pulse > 250 {
			httpresp.Error(w, http.StatusBadRequest, ErrPulseRange.Error())
			return
		}
		updated.Pulse = req.Pulse
	}
	if req.MeasuredAt != nil {
		updated.MeasuredAt = req.MeasuredAt.UTC()
	}

	if err := h.repo.UpdateEntry(r.Context(), &updated); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "blood pressure reading not found")
			return
		}
		if isValidationError(err) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "update blood pressure reading", err)
		return
	}
	httpresp.OK(w, "updated blood pressure reading", toDTO(updated))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	entryID := chi.URLParam(r, "id")
	if entryID == "" {
		httpresp.Error(w, http.StatusBadRequest, "blood pressure reading id is required")
		return
	}
	if err := h.repo.Delete(r.Context(), userID, entryID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "blood pressure reading not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete blood pressure reading", err)
		return
	}
	httpresp.OK(w, "deleted blood pressure reading", nil)
}

// --- helpers -------------------------------------------------------

// parseSinceUntil is a small local duplicate of bodyweight's helper of the
// same name. It's private there; a copy keeps this package self-contained
// rather than exporting a helper across domains.
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
	case errors.Is(err, ErrSystolicRange),
		errors.Is(err, ErrDiastolicRange),
		errors.Is(err, ErrPulseRange),
		errors.Is(err, ErrSystolicNotAboveDiastolic),
		errors.Is(err, ErrMeasuredAtRequired):
		return true
	}
	return false
}
