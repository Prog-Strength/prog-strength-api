package workout

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// Handler exposes HTTP endpoints for workout logging.
type Handler struct {
	repo Repository
}

// NewHandler builds a Handler backed by the given repository.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// Mount registers workout routes on the given router. Callers are expected
// to have already wrapped the router in auth.RequireUser middleware — these
// handlers read the user ID from request context and assume it is present.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workouts", func(r chi.Router) {
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Put("/{id}", h.update)
		r.Delete("/{id}", h.delete)
	})
}

// list handles GET /workouts. Returns the authed user's workouts, most
// recent first. The repository caps results at 50; pagination and
// filtering are intentionally not yet exposed (will be added when the
// data volume actually warrants it).
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	workouts, err := h.repo.ListByUser(r.Context(), userID, ListOptions{})
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list workouts", err)
		return
	}

	// Normalize nil to empty slice so empty results render as "data": []
	// rather than dropping the field via omitempty on the envelope.
	if workouts == nil {
		workouts = []Workout{}
	}

	httpresp.OK(w, "listed workouts", workouts)
}

// create handles POST /workouts.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		// Reaching this branch means the route was mounted without
		// RequireUser middleware — a wiring bug, not a user-facing error.
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	var req createWorkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := req.Name
	if name == "" {
		name = fmt.Sprintf("Workout - %s", time.Now().Format("Jan 02, 2006"))
	}

	var performedAt time.Time
	var err error
	if req.PerformedAt == "" {
		performedAt = time.Now()
	} else {
		performedAt, err = time.Parse(time.RFC3339, req.PerformedAt)
		if err != nil {
			httpresp.Error(w, http.StatusBadRequest, "invalid performed_at: must be RFC3339 format")
			return
		}
	}

	endedAt, err := parseOptionalRFC3339(req.EndedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid ended_at: must be RFC3339 format")
		return
	}

	workout := &Workout{
		UserID:      userID,
		Name:        name,
		PerformedAt: performedAt,
		EndedAt:     endedAt,
		Notes:       req.Notes,
		Exercises:   make([]WorkoutExercise, len(req.Exercises)),
	}
	for i, exReq := range req.Exercises {
		workout.Exercises[i] = WorkoutExercise{
			ExerciseID:    exReq.ExerciseID,
			Order:         i,
			SupersetGroup: exReq.SupersetGroup,
			Sets:          exReq.Sets,
			Notes:         exReq.Notes,
		}
	}

	if err := h.repo.Create(r.Context(), workout); err != nil {
		if mapWorkoutValidationError(w, err) {
			return
		}
		httpresp.ServerError(w, r.Context(), "create workout", err)
		return
	}

	httpresp.Created(w, "created workout", workout)
}

// update handles PUT /workouts/{id}. Full-replacement semantics: the body
// contains the complete workout state (same shape as POST), and we replace
// the existing record. ID and UserID are sourced from the URL and the
// authed user respectively — values in the body are ignored.
//
// Unlike create, no convenience defaults are applied. An update means the
// caller is intentionally setting state; silently substituting a date-stamped
// name or "now" for performed_at would let a small omission clobber data
// the user didn't intend to touch. Required fields (performed_at, exercises)
// must be present.
//
// Ownership is enforced by loading the existing workout first and comparing
// UserID. Mismatches return 404 (not 403) so attackers can't enumerate
// workout IDs to discover other users' data.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	workoutID := chi.URLParam(r, "id")
	if workoutID == "" {
		httpresp.Error(w, http.StatusBadRequest, "workout id is required")
		return
	}

	// Verify ownership before accepting the body. Treat "not yours" as 404.
	existing, err := h.repo.GetByID(r.Context(), workoutID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get workout", err)
		return
	}
	if existing.UserID != userID {
		httpresp.Error(w, http.StatusNotFound, "workout not found")
		return
	}

	var req createWorkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.PerformedAt == "" {
		httpresp.Error(w, http.StatusBadRequest, "performed_at is required")
		return
	}
	performedAt, err := time.Parse(time.RFC3339, req.PerformedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid performed_at: must be RFC3339 format")
		return
	}

	endedAt, err := parseOptionalRFC3339(req.EndedAt)
	if err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid ended_at: must be RFC3339 format")
		return
	}

	workout := &Workout{
		ID:          workoutID,
		UserID:      userID,
		Name:        req.Name,
		PerformedAt: performedAt,
		EndedAt:     endedAt,
		Notes:       req.Notes,
		Exercises:   make([]WorkoutExercise, len(req.Exercises)),
	}
	for i, exReq := range req.Exercises {
		workout.Exercises[i] = WorkoutExercise{
			ExerciseID:    exReq.ExerciseID,
			Order:         i,
			SupersetGroup: exReq.SupersetGroup,
			Sets:          exReq.Sets,
			Notes:         exReq.Notes,
		}
	}

	if err := h.repo.Update(r.Context(), workout); err != nil {
		if mapWorkoutValidationError(w, err) {
			return
		}
		if errors.Is(err, ErrNotFound) {
			// Race: deleted between our GetByID and Update.
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "update workout", err)
		return
	}

	httpresp.OK(w, "updated workout", workout)
}

// delete handles DELETE /workouts/{id}. Soft-deletes the workout via the
// repo (sets DeletedAt); subsequent reads treat the row as gone. Ownership
// is enforced with a GetByID-then-compare pattern that returns 404 on
// mismatch, matching update — never reveal the existence of other users'
// workouts to ID enumeration.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	workoutID := chi.URLParam(r, "id")
	if workoutID == "" {
		httpresp.Error(w, http.StatusBadRequest, "workout id is required")
		return
	}

	existing, err := h.repo.GetByID(r.Context(), workoutID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "get workout", err)
		return
	}
	if existing.UserID != userID {
		httpresp.Error(w, http.StatusNotFound, "workout not found")
		return
	}

	if err := h.repo.Delete(r.Context(), workoutID); err != nil {
		if errors.Is(err, ErrNotFound) {
			// Race: deleted between our GetByID and Delete.
			httpresp.Error(w, http.StatusNotFound, "workout not found")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete workout", err)
		return
	}

	httpresp.OK(w, "deleted workout", nil)
}

// createWorkoutRequest is the request body for POST /workouts (and PUT).
type createWorkoutRequest struct {
	Name        string                  `json:"name"`
	PerformedAt string                  `json:"performed_at"` // RFC3339
	EndedAt     string                  `json:"ended_at"`     // RFC3339, optional
	Notes       string                  `json:"notes"`
	Exercises   []createWorkoutExercise `json:"exercises"`
}

type createWorkoutExercise struct {
	ExerciseID    string `json:"exercise_id"`
	SupersetGroup *int   `json:"superset_group"` // optional; nil = standalone
	Notes         string `json:"notes"`
	Sets          []Set  `json:"sets"`
}

// parseOptionalRFC3339 parses an RFC3339 timestamp string, returning nil for
// an empty string. Used for optional timestamp fields like ended_at where
// "field absent" and "field present but invalid" must be distinguished.
func parseOptionalRFC3339(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// mapWorkoutValidationError writes a 400 response if err is a known workout
// validation error and returns true. Returns false for unknown errors so the
// caller can decide (typically: log and respond 500). Centralized so create
// and update don't duplicate the long error chain.
func mapWorkoutValidationError(w http.ResponseWriter, err error) bool {
	var invalidEnumErr *InvalidEnumError
	if errors.As(err, &invalidEnumErr) || errors.Is(err, ErrUserIDRequired) ||
		errors.Is(err, ErrPerformedAtRequired) || errors.Is(err, ErrEndedAtBeforeStart) ||
		errors.Is(err, ErrExercisesRequired) || errors.Is(err, ErrExerciseIDRequired) ||
		errors.Is(err, ErrInvalidOrder) || errors.Is(err, ErrSetsRequired) ||
		errors.Is(err, ErrInvalidReps) || errors.Is(err, ErrInvalidWeight) {
		httpresp.Error(w, http.StatusBadRequest, err.Error())
		return true
	}
	return false
}
