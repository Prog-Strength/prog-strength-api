package workout

import (
	"errors"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// TestWorkoutValidate_ZeroExercisesAllowed proves the relaxed rule: a workout
// with no exercises now validates (it's one created from a TCX before the user
// fills it in). UserID and PerformedAt are still required.
func TestWorkoutValidate_ZeroExercisesAllowed(t *testing.T) {
	w := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 6, 19, 13, 0, 0, 0, time.UTC),
		Exercises:   nil,
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("zero-exercise workout should validate, got %v", err)
	}
}

func TestWorkoutValidate_StillRequiresUserAndPerformedAt(t *testing.T) {
	missingUser := &Workout{PerformedAt: time.Now()}
	if err := missingUser.Validate(); !errors.Is(err, ErrUserIDRequired) {
		t.Errorf("missing user: err = %v, want ErrUserIDRequired", err)
	}

	missingPerformedAt := &Workout{UserID: "u1"}
	if err := missingPerformedAt.Validate(); !errors.Is(err, ErrPerformedAtRequired) {
		t.Errorf("missing performed_at: err = %v, want ErrPerformedAtRequired", err)
	}
}

// TestWorkoutValidate_PerExerciseStillValidated proves removing the
// "at least one exercise" rule didn't weaken per-exercise validation: a
// present-but-invalid exercise is still rejected.
func TestWorkoutValidate_PerExerciseStillValidated(t *testing.T) {
	w := &Workout{
		UserID:      "u1",
		PerformedAt: time.Now(),
		Exercises: []WorkoutExercise{
			{
				ExerciseID: "barbell-bench-press",
				Order:      0,
				Sets:       []Set{{Reps: 0, Weight: 100, Unit: user.WeightUnitPounds}}, // reps must be positive
			},
		},
	}
	if err := w.Validate(); !errors.Is(err, ErrInvalidReps) {
		t.Errorf("invalid reps in a present exercise should fail, got %v", err)
	}
}
