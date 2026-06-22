package snapshot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// Tiny fakes for the narrow consumer interfaces. Each carries the rows to
// return plus an optional err, so a single field flip simulates one
// domain's repo failing.

type fakeWorkout struct {
	workouts []workout.Workout
	prs      []workout.PersonalRecordEvent
	err      error
}

func (f fakeWorkout) ListByUser(_ context.Context, _ string, _ workout.ListOptions) ([]workout.Workout, error) {
	return f.workouts, f.err
}

func (f fakeWorkout) ListPersonalRecordEventsByWorkouts(_ context.Context, _ []string) ([]workout.PersonalRecordEvent, error) {
	return f.prs, f.err
}

type fakeExercise struct {
	exercises []exercise.Exercise
	err       error
}

func (f fakeExercise) List(_ context.Context, _ exercise.ListOptions) ([]exercise.Exercise, error) {
	return f.exercises, f.err
}

type fakeActivity struct {
	activities []activity.Activity
	bests      []activity.RunningBestEffort
	err        error
}

func (f fakeActivity) ListInRange(_ context.Context, _ string, _, _ *time.Time) ([]activity.Activity, error) {
	return f.activities, f.err
}

func (f fakeActivity) GetUserRunningBestEfforts(_ context.Context, _ string) ([]activity.RunningBestEffort, error) {
	return f.bests, f.err
}

type fakeSteps struct {
	entries []steps.Entry
	goal    steps.Goal
	err     error
}

func (f fakeSteps) List(_ context.Context, _ string, _, _ *string, _ int, _ *string) ([]steps.Entry, string, error) {
	return f.entries, "", f.err
}

func (f fakeSteps) GetGoal(_ context.Context, _ string) (steps.Goal, error) {
	return f.goal, f.err
}

type fakeBodyweight struct {
	entries []bodyweight.Entry
	err     error
}

func (f fakeBodyweight) List(_ context.Context, _ string, _, _ *time.Time) ([]bodyweight.Entry, error) {
	return f.entries, f.err
}

type fakeNutrition struct {
	days  []nutrition.DailyMacros
	goals nutrition.MacroGoals
	err   error
}

func (f fakeNutrition) DailyMacros(_ context.Context, _ string, _, _ time.Time, _ *time.Location) ([]nutrition.DailyMacros, error) {
	return f.days, f.err
}

func (f fakeNutrition) GetMacroGoals(_ context.Context, _ string) (nutrition.MacroGoals, error) {
	return f.goals, f.err
}

type fakeUser struct {
	u   *user.User
	err error
}

func (f fakeUser) GetByID(_ context.Context, _ string) (*user.User, error) {
	return f.u, f.err
}

func TestBuild_FanOutHappyPath(t *testing.T) {
	loc := mustLoc(t)
	start := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)

	bench := exercise.Exercise{ID: "barbell-bench-press", Name: "barbell bench press",
		MuscleGroups: []exercise.MuscleGroup{exercise.MuscleChest}}
	w := workout.Workout{ID: "w1", Name: "Push Day",
		PerformedAt: time.Date(2026, 6, 16, 18, 0, 0, 0, time.UTC),
		Exercises: []workout.WorkoutExercise{{ExerciseID: "barbell-bench-press", Sets: []workout.Set{
			{Reps: 8, Weight: 225}, {Reps: 8, Weight: 265},
		}}}}
	prs := []workout.PersonalRecordEvent{{ExerciseID: "barbell-bench-press", Weight: 265, Reps: 8, WorkoutID: "w1"}}

	runStart := time.Date(2026, 6, 17, 13, 0, 0, 0, time.UTC)
	pace := 315.0
	acts := []activity.Activity{{
		ActivityType: activity.ActivityRunning, StartTime: runStart, DistanceMeters: 8000,
		DurationSeconds: 2520, AvgPaceSecPerKm: &pace, Name: strptr("Easy 5"),
	}}
	bests := []activity.RunningBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1320, ActivityStartTime: runStart},
	}

	stepEntries := []steps.Entry{
		{Date: "2026-06-15", Steps: 8800},
		{Date: "2026-06-16", Steps: 9440},
	}
	bwEntries := []bodyweight.Entry{
		{Weight: 184.2, Unit: user.WeightUnitPounds, MeasuredAt: time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)},
		{Weight: 183.4, Unit: user.WeightUnitPounds, MeasuredAt: time.Date(2026, 6, 20, 7, 0, 0, 0, time.UTC)},
	}
	nutDays := []nutrition.DailyMacros{
		{Date: "2026-06-15", Calories: 2710, ProteinG: 205, FatG: 79, CarbsG: 288},
	}

	svc := NewService(
		fakeWorkout{workouts: []workout.Workout{w}, prs: prs},
		fakeExercise{exercises: []exercise.Exercise{bench}},
		fakeActivity{activities: acts, bests: bests},
		fakeSteps{entries: stepEntries, goal: steps.Goal{Goal: 10000}},
		fakeBodyweight{entries: bwEntries},
		fakeNutrition{days: nutDays, goals: nutrition.MacroGoals{Calories: 2700, ProteinG: 200, FatG: 80, CarbsG: 295}},
		fakeUser{u: &user.User{WeightUnit: user.WeightUnitPounds}},
	)

	snap := svc.Build(context.Background(), "u1", start, end, loc)

	// Period: localized to America/Denver (start - 6h = same local day).
	if snap.Period.StartDate != "2026-06-15" || snap.Period.EndDate != "2026-06-21" {
		t.Fatalf("period dates = %+v", snap.Period)
	}
	if snap.Period.Days != 7 || snap.Period.Timezone != "America/Denver" {
		t.Fatalf("period = %+v", snap.Period)
	}

	if snap.Strength == nil {
		t.Fatal("strength nil")
	}
	if snap.Strength.SessionCount != 1 || snap.Strength.TotalVolume != 225*8+265*8 {
		t.Fatalf("strength = %+v", snap.Strength)
	}
	if snap.Strength.Unit != "lb" {
		t.Fatalf("strength unit = %q", snap.Strength.Unit)
	}
	if len(snap.Strength.HeadlinePRs) != 1 {
		t.Fatalf("headline prs = %+v", snap.Strength.HeadlinePRs)
	}

	if snap.Running == nil || snap.Running.RunCount != 1 || snap.Running.TotalDistanceM != 8000 {
		t.Fatalf("running = %+v", snap.Running)
	}
	if len(snap.Running.NewBestEfforts) != 1 {
		t.Fatalf("new bests = %+v", snap.Running.NewBestEfforts)
	}

	if snap.Steps == nil || snap.Steps.DaysLogged != 2 || snap.Steps.Total != 18240 || snap.Steps.Goal != 10000 {
		t.Fatalf("steps = %+v", snap.Steps)
	}

	if snap.Bodyweight == nil || snap.Bodyweight.Start != 184.2 || snap.Bodyweight.End != 183.4 {
		t.Fatalf("bodyweight = %+v", snap.Bodyweight)
	}

	if snap.Nutrition == nil || snap.Nutrition.DaysLogged != 1 || snap.Nutrition.Goals.Calories != 2700 {
		t.Fatalf("nutrition = %+v", snap.Nutrition)
	}

	// Consistency: distinct active local days across strength (6-16),
	// running (6-17) and non-zero step days (6-15, 6-16) = {15,16,17} = 3.
	if snap.Consistency.WindowDays != 7 || snap.Consistency.ActiveDays != 3 {
		t.Fatalf("consistency = %+v", snap.Consistency)
	}
}

func TestBuild_DefensiveDegradation_WorkoutError(t *testing.T) {
	loc := mustLoc(t)
	start := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	svc := NewService(
		fakeWorkout{err: errors.New("boom")},
		fakeExercise{}, fakeActivity{}, fakeSteps{}, fakeBodyweight{}, fakeNutrition{},
		fakeUser{u: &user.User{WeightUnit: user.WeightUnitPounds}},
	)
	snap := svc.Build(context.Background(), "u1", start, end, loc)
	if snap.Strength != nil {
		t.Fatal("strength should be nil on workout repo error")
	}
	if snap.Running == nil || snap.Steps == nil || snap.Bodyweight == nil || snap.Nutrition == nil {
		t.Fatal("other sections should survive one domain's failure")
	}
}

func TestBuild_DefensiveDegradation_NutritionError(t *testing.T) {
	loc := mustLoc(t)
	start := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	svc := NewService(
		fakeWorkout{}, fakeExercise{}, fakeActivity{}, fakeSteps{}, fakeBodyweight{},
		fakeNutrition{err: errors.New("db exploded")},
		fakeUser{u: &user.User{WeightUnit: user.WeightUnitPounds}},
	)
	snap := svc.Build(context.Background(), "u1", start, end, loc)
	if snap.Nutrition != nil {
		t.Fatal("nutrition should be nil on nutrition repo error")
	}
	if snap.Strength == nil || snap.Running == nil || snap.Steps == nil || snap.Bodyweight == nil {
		t.Fatal("other sections should survive nutrition's failure")
	}
}

func TestBuild_EmptyButHealthy_RendersNonNil(t *testing.T) {
	loc := mustLoc(t)
	start := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	svc := NewService(fakeWorkout{}, fakeExercise{}, fakeActivity{}, fakeSteps{}, fakeBodyweight{},
		fakeNutrition{}, fakeUser{u: &user.User{WeightUnit: user.WeightUnitPounds}})
	snap := svc.Build(context.Background(), "u1", start, end, loc)
	if snap.Strength == nil || snap.Running == nil || snap.Steps == nil ||
		snap.Bodyweight == nil || snap.Nutrition == nil {
		t.Fatal("empty-but-healthy domains must render non-nil")
	}
	if snap.Strength.SessionCount != 0 || snap.Running.RunCount != 0 || snap.Steps.DaysLogged != 0 {
		t.Fatalf("empty domains should have zero counts: %+v %+v %+v", snap.Strength, snap.Running, snap.Steps)
	}
	if snap.Period.Days != 7 || snap.Consistency.WindowDays != 7 || snap.Consistency.ActiveDays != 0 {
		t.Fatalf("period/consistency = %+v %+v", snap.Period, snap.Consistency)
	}
}
