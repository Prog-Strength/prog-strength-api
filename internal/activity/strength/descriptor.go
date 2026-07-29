package strength

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

// NewDescriptor builds the strength_training descriptor for the activity
// type registry. The strength package owns it (the parent internal/activity
// must never import this child), and server.go passes it to NewRegistry
// alongside the endurance descriptors — after binding Create/Update/
// MountRoutes to the workout handler (Handler.CreateSession,
// Handler.UpdateSession, Handler.MountActivityRoutes), so the unified
// surface reuses the exact POST/PUT /workouts machinery instead of
// reimplementing it. repo may be nil in tests that only exercise validation
// or summaries.
func NewDescriptor(repo Repository) *activity.Descriptor {
	return &activity.Descriptor{
		Type:           activity.ActivityStrengthTraining,
		ValidateCreate: validateCreate,
		Details:        &detailStore{repo: repo},
		Summarize:      summarize,
		DecodeDetails:  decodeDetails,
	}
}

// Details is the strength detail payload the unified surface reads and
// writes: the same exercises shape POST /workouts accepts (order assigned
// from slice position, like the workout handler does). One struct for both
// directions so what a client POSTs as `details` is what GET returns.
//
// PersonalRecordsSet is read-only enrichment (stage-3 parity with the
// /workouts DTOs): the PR break events this session produced, in the exact
// legacy personalRecordEventDTO shape so web maps 1:1. The store's Load/
// LoadMany populate it (never nil — [] when no PRs, like the legacy list);
// the write decode path ignores it (writes carry only exercises).
type Details struct {
	Exercises          []WorkoutExercise        `json:"exercises"`
	PersonalRecordsSet []personalRecordEventDTO `json:"personal_records_set"`
}

// detailsPayload is the decode shape for the Details blob (the create-
// request exercise DTO, whose slice position becomes Order).
type detailsPayload struct {
	Exercises []createWorkoutExercise `json:"exercises"`
}

// parseDetails decodes a Details blob into the domain exercise slice.
// Absent/JSON-null details are a zero-exercise lift — allowed, mirroring
// Workout.Validate (a session created before its exercises are filled in).
// Decode failures wrap activity.ErrInvalidDetails per the DetailStore error
// contract.
func parseDetails(raw json.RawMessage) ([]WorkoutExercise, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p detailsPayload
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: invalid strength details: %w", activity.ErrInvalidDetails, err)
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

// decodeDetails is the descriptor's DecodeDetails: the *Details value the
// detail store saves and Summarize renders. Absent details decode to nil
// (the unified generic path then clears/skips) — though in practice
// strength writes flow through Create/Update, not this seam.
func decodeDetails(raw json.RawMessage) (any, error) {
	exercises, err := parseDetails(raw)
	if err != nil {
		return nil, err
	}
	if exercises == nil {
		return nil, nil
	}
	return &Details{Exercises: exercises}, nil
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

// owned fetches the workout and enforces ownership. Missing/deleted rows and
// wrong owners are indistinguishable (house rule) and surface as the BASE
// package's activity.ErrNotFound — the DetailStore contract promises one
// sentinel across every implementation so the unified handler's error
// mapping never branches per type. The repository's own strength.ErrNotFound
// is translated at this seam.
func (s *detailStore) owned(ctx context.Context, userID, activityID string) (*Workout, error) {
	w, err := s.repo.GetByID(ctx, activityID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, activity.ErrNotFound
		}
		return nil, err
	}
	if w.UserID != userID {
		return nil, activity.ErrNotFound
	}
	return w, nil
}

// Load returns the full strength detail payload for one owned session:
// the exercises off the hydrated workout plus its PR break events (stage-3
// /workouts parity) — so a detail read costs the ownership read plus one
// PR-event query, exactly like the legacy GET /workouts/{id}.
func (s *detailStore) Load(ctx context.Context, userID, activityID string) (any, error) {
	w, err := s.owned(ctx, userID, activityID)
	if err != nil {
		return nil, err
	}
	events, err := s.prEventsByWorkout(ctx, []string{activityID})
	if err != nil {
		return nil, err
	}
	return &Details{Exercises: w.Exercises, PersonalRecordsSet: events[activityID]}, nil
}

// LoadMany is the BulkDetailLoader capability the unified list uses: one
// batched read (the repository's two-statement exercise hydration) plus one
// bulk PR-event read for a whole page of lifts — never a per-row query. Per
// the seam's contract the ids are already user-scoped by the caller's base
// list read, so no per-id ownership check re-runs here.
func (s *detailStore) LoadMany(ctx context.Context, _ string, ids []string) (map[string]any, error) {
	byWorkout, err := s.repo.ListExercisesByWorkoutIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	events, err := s.prEventsByWorkout(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(byWorkout))
	for id, exercises := range byWorkout {
		out[id] = &Details{Exercises: exercises, PersonalRecordsSet: events[id]}
	}
	return out, nil
}

// prEventsByWorkout bulk-loads the PR break events for a set of workout ids
// (the same ListPersonalRecordEventsByWorkouts query the legacy /workouts
// list uses), rendered as the legacy DTO and keyed by workout id. Every
// requested id gets a non-nil slice so the wire shape is always [] — the
// legacy DTOs never emit null here.
func (s *detailStore) prEventsByWorkout(ctx context.Context, ids []string) (map[string][]personalRecordEventDTO, error) {
	events, err := s.repo.ListPersonalRecordEventsByWorkouts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]personalRecordEventDTO, len(ids))
	for _, id := range ids {
		out[id] = []personalRecordEventDTO{}
	}
	for _, e := range events {
		out[e.WorkoutID] = append(out[e.WorkoutID], eventToDTO(e))
	}
	return out, nil
}

// Save replaces the session's exercise list wholesale — the same
// full-replacement semantics as PUT /workouts, routed through Update so PR
// detection and 1RM history stay on the one write path. An Update error is
// translated to the base sentinel like every other seam error (the row can
// vanish between the ownership read and the write).
func (s *detailStore) Save(ctx context.Context, userID, activityID string, details any) error {
	d, ok := details.(*Details)
	if !ok {
		return fmt.Errorf("strength: detail store got %T, want *Details", details)
	}
	w, err := s.owned(ctx, userID, activityID)
	if err != nil {
		return err
	}
	w.Exercises = d.Exercises
	if err := s.repo.Update(ctx, w); err != nil {
		if errors.Is(err, ErrNotFound) {
			return activity.ErrNotFound
		}
		return err
	}
	return nil
}

// Delete clears the session's exercises/sets. The base row survives — soft
// deleting the activity itself is the base domain's job.
func (s *detailStore) Delete(ctx context.Context, userID, activityID string) error {
	return s.Save(ctx, userID, activityID, &Details{Exercises: []WorkoutExercise{}})
}

// summarize renders the workout card the timeline hydrator renders today:
// title, "N exercises" subtitle, and exercises/sets/volume chips (zero-value
// chips omitted, matching the hydrator).
func summarize(a activity.Activity, details any) activity.Summary {
	var exercises []WorkoutExercise
	if d, ok := details.(*Details); ok && d != nil {
		exercises = d.Exercises
	}

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
