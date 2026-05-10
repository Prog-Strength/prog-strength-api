package workout

// WorkoutExercise represents one exercise performed within a workout —
// e.g., "Back Squat" within today's session, containing all the sets
// the user performed of that exercise.
//
// SupersetGroup is a nullable identifier; exercises sharing a value were
// performed as a superset (alternating sets across the group). Standalone
// exercises leave it nil. The integer is opaque — any value works, so the
// frontend picks the convention (commonly 1, 2, 3... per workout).
type WorkoutExercise struct {
	ExerciseID    string `json:"exercise_id"` // references exercise.Exercise.ID
	Order         int    `json:"order"`       // position within the workout (0-indexed)
	SupersetGroup *int   `json:"superset_group,omitempty"`
	Sets          []Set  `json:"sets"`
	Notes         string `json:"notes,omitempty"`
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
