package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Handler exposes HTTP endpoints for workout logging.
type Handler struct {
	repo Repository
}

// NewHandler builds a Handler backed by the given repository.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// Mount registers workout routes on the given router.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workouts", func(r chi.Router) {
		r.Post("/", h.create)
	})
}

// create handles POST /workouts.
// DEV-ONLY: Reads user ID from X-User-ID header until OAuth is implemented.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	// DEV-ONLY: Extract user ID from header until OAuth middleware is in place.
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "X-User-ID header required (dev-only)")
		return
	}

	var req createWorkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Default name if not provided.
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("Workout - %s", time.Now().Format("Jan 02, 2006"))
	}

	// Parse performed_at time.
	var performedAt time.Time
	var err error
	if req.PerformedAt == "" {
		// Default to now if not provided.
		performedAt = time.Now()
	} else {
		performedAt, err = time.Parse(time.RFC3339, req.PerformedAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid performed_at: must be RFC3339 format")
			return
		}
	}

	// Build Workout from request.
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
			Order:      i, // Auto-assign order based on position in array.
			Sets:       exReq.Sets,
			Notes:      exReq.Notes,
		}
	}

	// Create the workout.
	if err := h.repo.Create(r.Context(), workout); err != nil {
		// Validation errors from Validate() should be returned as 400.
		var invalidEnumErr *InvalidEnumError
		if errors.As(err, &invalidEnumErr) || errors.Is(err, ErrUserIDRequired) ||
			errors.Is(err, ErrPerformedAtRequired) || errors.Is(err, ErrExercisesRequired) ||
			errors.Is(err, ErrExerciseIDRequired) || errors.Is(err, ErrInvalidOrder) ||
			errors.Is(err, ErrSetsRequired) || errors.Is(err, ErrInvalidReps) ||
			errors.Is(err, ErrInvalidWeight) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeServerError(w, r.Context(), "create workout", err)
		return
	}

	writeJSON(w, http.StatusCreated, workout)
}

// createWorkoutRequest is the request body for POST /workouts.
type createWorkoutRequest struct {
	Name        string                    `json:"name"`
	PerformedAt string                    `json:"performed_at"` // RFC3339 format
	Notes       string                    `json:"notes"`
	Exercises   []createWorkoutExercise   `json:"exercises"`
}

type createWorkoutExercise struct {
	ExerciseID string `json:"exercise_id"`
	Notes      string `json:"notes"`
	Sets       []Set  `json:"sets"`
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
