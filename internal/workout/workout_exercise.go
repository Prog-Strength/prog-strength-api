package workout

// WorkoutExercise represents one exercise performed within a workout —
// e.g., "Back Squat" within today's session, containing all the sets
// the user performed of that exercise.
type WorkoutExercise struct {
	ExerciseID string `json:"exercise_id"` // references exercise.Exercise.ID
	Order      int    `json:"order"`       // position within the workout (0-indexed)
	Sets       []Set  `json:"sets"`
	Notes      string `json:"notes,omitempty"`
}

func (we *WorkoutExercise) Validate() error {
	if we.ExerciseID == "" {
		return ErrExerciseIDRequired
	}
	if we.Order < 0 {
		return ErrInvalidOrder
	}
	if len(we.Sets) == 0 {
		return ErrSetsRequired
	}
	for i := range we.Sets {
		if err := we.Sets[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}
