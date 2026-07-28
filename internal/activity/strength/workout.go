package strength

import "time"

// Workout is a single training session performed by a user on a given date,
// composed of one or more exercises with their sets. Since the unified
// activity model (migration 042) it is stored as a strength_training row in
// the activities base table plus activity_exercises/sets children.
//
// PerformedAt is the start time (the row's start_time). EndedAt is derived on
// read from start_time + duration_seconds (nil when duration is NULL — an
// end-less lift); on write the repository stores EndedAt − PerformedAt as
// duration_seconds. The DTO shape (performed_at/ended_at) is unchanged.
type Workout struct {
	ID          string            `json:"id"`
	UserID      string            `json:"user_id"`
	Name        string            `json:"name,omitempty"`
	PerformedAt time.Time         `json:"performed_at"`
	EndedAt     *time.Time        `json:"ended_at,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Exercises   []WorkoutExercise `json:"exercises"`
	// ActivityID is derived on read, kept for API compatibility through the
	// shim period: it is the workout's OWN id when a TCX enrichment is
	// attached (the row's tcx_s3_key is non-NULL) and nil otherwise. The old
	// dual-row link column is gone — the enrichment lives on this workout's
	// base row itself, so "the enrichment activity" IS the workout.
	ActivityID *string    `json:"activity_id"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"-"`
}

func (w *Workout) Validate() error {
	if w.UserID == "" {
		return ErrUserIDRequired
	}
	if w.PerformedAt.IsZero() {
		return ErrPerformedAtRequired
	}
	if w.EndedAt != nil && w.EndedAt.Before(w.PerformedAt) {
		return ErrEndedAtBeforeStart
	}
	// A workout may persist with zero exercises: it's one the user hasn't
	// filled in yet (e.g. created from a TCX, exercises added afterward).
	// The detail page renders an empty exercise state with "+ Add exercise".
	// Per-exercise validation below still applies to any exercises present.
	for i := range w.Exercises {
		if err := w.Exercises[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}
