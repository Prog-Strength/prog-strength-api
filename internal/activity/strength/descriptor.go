package strength

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

// NewDescriptor builds the strength_training descriptor for the activity
// type registry. The strength package owns it (the parent internal/activity
// must never import this child), and server.go passes it to NewRegistry
// alongside the endurance descriptors.
//
// MountRoutes is nil for now: /workouts/* stays mounted by Handler until
// the unified /activities surface re-mounts the strength routes through the
// registry. repo may be nil in tests that only exercise validation or
// summaries.
func NewDescriptor(repo Repository) *activity.Descriptor {
	return &activity.Descriptor{
		Type:           activity.ActivityStrengthTraining,
		ValidateCreate: validateCreate,
		Details:        &detailStore{repo: repo},
		Summarize:      summarize,
	}
}

// detailsPayload is the strength Details blob for the unified create/update
// path: the same exercises shape POST /workouts accepts (order assigned
// from slice position, like the workout handler does).
type detailsPayload struct {
	Exercises []createWorkoutExercise `json:"exercises"`
}

// parseDetails decodes a Details blob into the domain exercise slice.
// Absent/JSON-null details are a zero-exercise lift — allowed, mirroring
// Workout.Validate (a session created before its exercises are filled in).
func parseDetails(raw json.RawMessage) ([]WorkoutExercise, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p detailsPayload
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid strength details: %w", err)
	}
	out := make([]WorkoutExercise, len(p.Exercises))
	for i, ex := range p.Exercises {
		out[i] = WorkoutExercise{
			ExerciseID:    ex.ExerciseID,
			Order:         i,
			SupersetGroup: ex.SupersetGroup,
			Notes:         ex.Notes,
			Sets:          ex.Sets,
		}
	}
	return out, nil
}

// validateCreate parses the Details blob and runs the same per-exercise
// validation Workout.Validate applies (its exercise loop, extracted here
// because the base fields Workout.Validate also checks — user, start time —
// are the unified handler's job, not the descriptor's).
func validateCreate(req activity.CreateRequest) error {
	exercises, err := parseDetails(req.Details)
	if err != nil {
		return err
	}
	for i := range exercises {
		if err := exercises[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}

// detailStore adapts the existing workout repository to the registry's
// DetailStore seam. The repository already owns the activity_exercises/sets
// persistence (batched load, replace-on-update), so each method is a thin
// ownership-checked wrapper; the `any` payload is []WorkoutExercise.
type detailStore struct {
	repo Repository
}

var _ activity.DetailStore = (*detailStore)(nil)

// owned fetches the workout and enforces ownership: a wrong user reads as
// ErrNotFound, indistinguishable from a missing row (house rule).
func (s *detailStore) owned(ctx context.Context, userID, activityID string) (*Workout, error) {
	w, err := s.repo.GetByID(ctx, activityID)
	if err != nil {
		return nil, err
	}
	if w.UserID != userID {
		return nil, ErrNotFound
	}
	return w, nil
}

func (s *detailStore) Load(ctx context.Context, userID, activityID string) (any, error) {
	w, err := s.owned(ctx, userID, activityID)
	if err != nil {
		return nil, err
	}
	return w.Exercises, nil
}

// Save replaces the session's exercise list wholesale — the same
// full-replacement semantics as PUT /workouts, routed through Update so PR
// detection and 1RM history stay on the one write path.
func (s *detailStore) Save(ctx context.Context, userID, activityID string, details any) error {
	exercises, ok := details.([]WorkoutExercise)
	if !ok {
		return fmt.Errorf("strength: detail store got %T, want []WorkoutExercise", details)
	}
	w, err := s.owned(ctx, userID, activityID)
	if err != nil {
		return err
	}
	w.Exercises = exercises
	return s.repo.Update(ctx, w)
}

// Delete clears the session's exercises/sets. The base row survives — soft
// deleting the activity itself is the base domain's job.
func (s *detailStore) Delete(ctx context.Context, userID, activityID string) error {
	return s.Save(ctx, userID, activityID, []WorkoutExercise{})
}

// summarize renders the workout card the timeline hydrator renders today:
// title, "N exercises" subtitle, and exercises/sets/volume chips (zero-value
// chips omitted, matching the hydrator).
func summarize(a activity.Activity, details any) activity.Summary {
	exercises, _ := details.([]WorkoutExercise)

	title := "Workout"
	if a.Name != nil && strings.TrimSpace(*a.Name) != "" {
		title = *a.Name
	}

	totalSets := 0
	var totalVolume float64
	for _, ex := range exercises {
		totalSets += len(ex.Sets)
		for _, s := range ex.Sets {
			totalVolume += s.Weight * float64(s.Reps)
		}
	}

	metrics := []string{activity.PluralCount(len(exercises), "exercise")}
	if totalSets > 0 {
		metrics = append(metrics, activity.PluralCount(totalSets, "set"))
	}
	if totalVolume > 0 {
		metrics = append(metrics, fmt.Sprintf("%s lb", activity.FormatThousands(totalVolume)))
	}

	return activity.Summary{
		Title:    title,
		Subtitle: activity.PluralCount(len(exercises), "exercise"),
		Metrics:  metrics,
	}
}
