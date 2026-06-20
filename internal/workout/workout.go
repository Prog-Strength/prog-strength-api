package workout

import "time"

// Workout is a single training session performed by a user on a given date,
// composed of one or more exercises with their sets.
//
// PerformedAt is the start time. EndedAt is optional; when present, session
// duration is EndedAt.Sub(PerformedAt). Stored as two timestamps rather than
// (start + duration) so the model is symmetric and either value is queryable
// directly.
type Workout struct {
	ID          string            `json:"id"`
	UserID      string            `json:"user_id"`
	Name        string            `json:"name,omitempty"`
	PerformedAt time.Time         `json:"performed_at"`
	EndedAt     *time.Time        `json:"ended_at,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Exercises   []WorkoutExercise `json:"exercises"`
	// ActivityID is a nullable soft reference to the activities row holding
	// this workout's Garmin TCX enrichment (heart rate, calories). Null when
	// no TCX is attached. There is no hard FK — the workout and activity
	// domains stay decoupled, matching how UserID is also un-FK'd. The
	// activities row carries the per-second HR trackpoints; the workout keeps
	// owning its exercises and sets.
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
