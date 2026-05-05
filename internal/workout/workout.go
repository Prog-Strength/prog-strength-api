package workout

import "time"

// Workout is a single training session performed by a user on a given date,
// composed of one or more exercises with their sets.
type Workout struct {
	ID          string            `json:"id"`
	UserID      string            `json:"user_id"`
	Name        string            `json:"name,omitempty"`
	PerformedAt time.Time         `json:"performed_at"`
	Notes       string            `json:"notes,omitempty"`
	Exercises   []WorkoutExercise `json:"exercises"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	DeletedAt   *time.Time        `json:"-"`
}

func (w *Workout) Validate() error {
	if w.UserID == "" {
		return ErrUserIDRequired
	}
	if w.PerformedAt.IsZero() {
		return ErrPerformedAtRequired
	}
	if len(w.Exercises) == 0 {
		return ErrExercisesRequired
	}
	for i := range w.Exercises {
		if err := w.Exercises[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}
