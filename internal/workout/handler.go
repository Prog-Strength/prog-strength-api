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
		r.Post("/", h.create)
	})
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

	workout := &Workout{
		UserID:      userID,
		Name:        name,
		PerformedAt: performedAt,
		Notes:       req.Notes,
		Exercises:   make([]WorkoutExercise, len(req.Exercises)),
	}
	for i, exReq := range req.Exercises {
		workout.Exercises[i] = WorkoutExercise{
			ExerciseID: exReq.ExerciseID,
			Order:      i,
			Sets:       exReq.Sets,
			Notes:      exReq.Notes,
		}
	}

	if err := h.repo.Create(r.Context(), workout); err != nil {
		var invalidEnumErr *InvalidEnumError
		if errors.As(err, &invalidEnumErr) || errors.Is(err, ErrUserIDRequired) ||
			errors.Is(err, ErrPerformedAtRequired) || errors.Is(err, ErrExercisesRequired) ||
			errors.Is(err, ErrExerciseIDRequired) || errors.Is(err, ErrInvalidOrder) ||
			errors.Is(err, ErrSetsRequired) || errors.Is(err, ErrInvalidReps) ||
			errors.Is(err, ErrInvalidWeight) {
			httpresp.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpresp.ServerError(w, r.Context(), "create workout", err)
		return
	}

	httpresp.Created(w, "created workout", workout)
}

// createWorkoutRequest is the request body for POST /workouts.
type createWorkoutRequest struct {
	Name        string                  `json:"name"`
	PerformedAt string                  `json:"performed_at"` // RFC3339 format
	Notes       string                  `json:"notes"`
	Exercises   []createWorkoutExercise `json:"exercises"`
}

type createWorkoutExercise struct {
	ExerciseID string `json:"exercise_id"`
	Notes      string `json:"notes"`
	Sets       []Set  `json:"sets"`
}
