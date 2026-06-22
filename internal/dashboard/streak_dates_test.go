package dashboard

import (
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// completedWorkout builds a workout that is finished (EndedAt set) and has at
// least one logged set — the shape that should count toward the streak.
func completedWorkout(start time.Time) workout.Workout {
	end := start.Add(time.Hour)
	return workout.Workout{
		PerformedAt: start,
		EndedAt:     &end,
		Exercises: []workout.WorkoutExercise{
			{ExerciseID: "ex1", Sets: []workout.Set{{Reps: 5, Weight: 100}}},
		},
	}
}

func TestStreakDates_CompletedWorkoutCounts(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	day := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	got := streakDates(nil, []workout.Workout{completedWorkout(day)}, nil, 0, denver)
	if !got["2026-06-17"] {
		t.Errorf("completed workout should mark its day active, got %v", got)
	}
}

func TestStreakDates_AbandonedWorkoutDoesNotCount(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	day := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// No EndedAt, no exercises — a started-then-abandoned session.
	w := workout.Workout{PerformedAt: day}
	got := streakDates(nil, []workout.Workout{w}, nil, 0, denver)
	if got["2026-06-17"] {
		t.Errorf("abandoned workout (no end, no sets) should not count, got %v", got)
	}
}

func TestStreakDates_FinishedButEmptyWorkoutDoesNotCount(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	day := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	end := day.Add(time.Hour)
	// EndedAt set but zero exercises — finished without logging anything.
	w := workout.Workout{PerformedAt: day, EndedAt: &end}
	got := streakDates(nil, []workout.Workout{w}, nil, 0, denver)
	if got["2026-06-17"] {
		t.Errorf("finished-but-empty workout should not count, got %v", got)
	}
}

func TestStreakDates_WorkoutWithSetsButNoEndDoesNotCount(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	day := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// Has a logged set but the user never finished the session.
	w := workout.Workout{
		PerformedAt: day,
		Exercises: []workout.WorkoutExercise{
			{ExerciseID: "ex1", Sets: []workout.Set{{Reps: 5, Weight: 100}}},
		},
	}
	got := streakDates(nil, []workout.Workout{w}, nil, 0, denver)
	if got["2026-06-17"] {
		t.Errorf("unfinished workout (no EndedAt) should not count, got %v", got)
	}
}

func TestStreakDates_CardioActivityTypesCount(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	for _, tc := range []struct {
		name string
		typ  activity.ActivityType
		date string
	}{
		{"running", activity.ActivityRunning, "2026-06-15"},
		{"walking", activity.ActivityWalking, "2026-06-16"},
		{"cycling", activity.ActivityCycling, "2026-06-17"},
		{"other", activity.ActivityOther, "2026-06-18"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, _ := time.ParseInLocation("2006-01-02", tc.date, denver)
			at = at.Add(13 * time.Hour)
			a := activity.Activity{ActivityType: tc.typ, StartTime: at}
			got := streakDates([]activity.Activity{a}, nil, nil, 0, denver)
			if !got[tc.date] {
				t.Errorf("%s activity should count, got %v", tc.name, got)
			}
		})
	}
}

func TestStreakDates_StrengthTrainingActivityDoesNotCount(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	day := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// strength_training activity rows are HR enrichment attached to a workout;
	// the workout-completion path is what counts, not this enrichment row.
	a := activity.Activity{ActivityType: activity.ActivityStrengthTraining, StartTime: day}
	got := streakDates([]activity.Activity{a}, nil, nil, 0, denver)
	if got["2026-06-17"] {
		t.Errorf("strength_training activity should not count on its own, got %v", got)
	}
}

func TestStreakDates_StepsCountOnlyWhenGoalMet(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	entries := []steps.Entry{
		{Date: "2026-06-15", Steps: 12000}, // >= goal → counts
		{Date: "2026-06-16", Steps: 4000},  // < goal → no
		{Date: "2026-06-17", Steps: 10000}, // == goal → counts
	}
	got := streakDates(nil, nil, entries, 10000, denver)
	if !got["2026-06-15"] {
		t.Errorf("steps above goal should count")
	}
	if got["2026-06-16"] {
		t.Errorf("steps below goal should not count")
	}
	if !got["2026-06-17"] {
		t.Errorf("steps equal to goal should count")
	}
}

func TestStreakDates_StepsNeverCountWhenGoalUnset(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	// goal == 0 means the user has never set a step goal; no day should count
	// off passive steps regardless of how high the count is.
	entries := []steps.Entry{{Date: "2026-06-15", Steps: 50000}}
	got := streakDates(nil, nil, entries, 0, denver)
	if got["2026-06-15"] {
		t.Errorf("steps should never count when no goal is set, got %v", got)
	}
}
