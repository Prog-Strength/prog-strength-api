package snapshot

import (
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func strptr(s string) *string { return &s }

func TestAggregateStrength_VolumeMuscleGroupsTopSetsPRs(t *testing.T) {
	loc := mustLoc(t)
	at := time.Date(2026, 6, 16, 18, 0, 0, 0, time.UTC)
	bench := exercise.Exercise{ID: "barbell-bench-press", Name: "barbell bench press",
		MuscleGroups: []exercise.MuscleGroup{exercise.MuscleChest, exercise.MuscleTriceps}}
	w := workout.Workout{ID: "w1", Name: "Chest & Back", PerformedAt: at,
		Exercises: []workout.WorkoutExercise{{ExerciseID: "barbell-bench-press", Sets: []workout.Set{
			{Reps: 8, Weight: 225}, {Reps: 8, Weight: 265},
		}}}}
	prs := []workout.PersonalRecordEvent{{ExerciseID: "barbell-bench-press", Weight: 265, Reps: 8, WorkoutID: "w1"}}
	got := aggregateStrength([]workout.Workout{w}, prs, []exercise.Exercise{bench}, "lb", loc)

	if got.SessionCount != 1 {
		t.Fatalf("session_count = %d, want 1", got.SessionCount)
	}
	if got.TotalVolume != 225*8+265*8 {
		t.Fatalf("total_volume = %v", got.TotalVolume)
	}
	// chest and triceps each get the exercise's 2 sets and full volume.
	if len(got.ByMuscleGroup) != 2 {
		t.Fatalf("by_muscle_group = %+v", got.ByMuscleGroup)
	}
	wantVol := float64(225*8 + 265*8)
	for _, mg := range got.ByMuscleGroup {
		if mg.Sets != 2 || mg.Volume != wantVol {
			t.Fatalf("muscle %s sets=%d volume=%v, want sets=2 volume=%v", mg.MuscleGroup, mg.Sets, mg.Volume, wantVol)
		}
	}
	if got.Sessions[0].Date != "2026-06-16" {
		t.Fatalf("session date = %q", got.Sessions[0].Date)
	}
	if got.Sessions[0].TopSets[0].Exercise != "barbell-bench-press" ||
		got.Sessions[0].TopSets[0].Weight != 265 {
		t.Fatalf("top set = %+v", got.Sessions[0].TopSets[0])
	}
	// Epley 1RM for 265x8 = 265*(1+8/30) ≈ 335.67 → rounded 336.
	if got.Sessions[0].TopSets[0].Est1RM != 336 {
		t.Fatalf("est_1rm = %v, want 336", got.Sessions[0].TopSets[0].Est1RM)
	}
	if len(got.Sessions[0].PRs) != 1 || got.Sessions[0].PRs[0].Kind != "weight" ||
		got.Sessions[0].PRs[0].Exercise != "barbell-bench-press" {
		t.Fatalf("session prs = %+v", got.Sessions[0].PRs)
	}
	if len(got.HeadlinePRs) != 1 || got.HeadlinePRs[0] != "265 lb barbell bench press PR" {
		t.Fatalf("headline prs = %+v", got.HeadlinePRs)
	}
}

// TopSets are ranked by Epley est-1RM and capped at 3 per session.
func TestAggregateStrength_TopSetsRankedAndCapped(t *testing.T) {
	loc := mustLoc(t)
	at := time.Date(2026, 6, 16, 18, 0, 0, 0, time.UTC)
	ex := func(id string) exercise.Exercise {
		return exercise.Exercise{ID: id, Name: id, MuscleGroups: []exercise.MuscleGroup{exercise.MuscleChest}}
	}
	exs := []exercise.Exercise{ex("a"), ex("b"), ex("c"), ex("d")}
	we := func(id string, weight float64) workout.WorkoutExercise {
		return workout.WorkoutExercise{ExerciseID: id, Sets: []workout.Set{{Reps: 1, Weight: weight}}}
	}
	// Est-1RM for a single rep equals weight, so ordering is by weight.
	w := workout.Workout{ID: "w1", PerformedAt: at, Exercises: []workout.WorkoutExercise{
		we("a", 100), we("b", 400), we("c", 200), we("d", 300),
	}}
	got := aggregateStrength([]workout.Workout{w}, nil, exs, "lb", loc)
	top := got.Sessions[0].TopSets
	if len(top) != maxTopSetsPerSession {
		t.Fatalf("top sets len = %d, want %d", len(top), maxTopSetsPerSession)
	}
	if top[0].Exercise != "b" || top[1].Exercise != "d" || top[2].Exercise != "c" {
		t.Fatalf("top set ranking = %+v", top)
	}
}

func TestAggregateStrength_EmptyIsNonNil(t *testing.T) {
	loc := mustLoc(t)
	got := aggregateStrength(nil, nil, nil, "lb", loc)
	if got == nil {
		t.Fatal("empty strength must be non-nil")
	}
	if got.SessionCount != 0 || got.ByMuscleGroup == nil || got.Sessions == nil || got.HeadlinePRs == nil {
		t.Fatalf("empty strength = %+v", got)
	}
}

func TestAggregateRunning_TotalsPaceAndNewBests(t *testing.T) {
	loc := mustLoc(t)
	start := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	runStart := time.Date(2026, 6, 17, 13, 0, 0, 0, time.UTC)
	pace := 315.0
	runs := []activity.Activity{
		{
			ActivityType: activity.ActivityRunning, StartTime: runStart, DistanceMeters: 8000,
			DurationSeconds: 2520, AvgPaceSecPerKm: &pace, Name: strptr("Easy 5"),
		},
		// Non-running activity is filtered out.
		{ActivityType: activity.ActivityWalking, StartTime: runStart, DistanceMeters: 3000, DurationSeconds: 1800},
	}
	bests := []activity.RunningBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1320, ActivityStartTime: runStart},                                     // in window → new
		{DistanceKey: "10k", DurationSeconds: 3000, ActivityStartTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, // old
	}
	got := aggregateRunning(runs, bests, start, end, loc)
	if got.RunCount != 1 || got.TotalDistanceM != 8000 || got.TotalDurationS != 2520 {
		t.Fatalf("totals = %+v", got)
	}
	// Section avg pace = totalDuration / (totalDistance / 1000) = 2520 / 8 = 315.
	if got.AvgPaceSecPerKm == nil || *got.AvgPaceSecPerKm != 315 {
		t.Fatalf("avg pace = %v", got.AvgPaceSecPerKm)
	}
	if len(got.Runs) != 1 || got.Runs[0].Date != "2026-06-17" || got.Runs[0].Name != "Easy 5" {
		t.Fatalf("runs = %+v", got.Runs)
	}
	if len(got.NewBestEfforts) != 1 || got.NewBestEfforts[0].DistanceKey != "5k" ||
		got.NewBestEfforts[0].TimeSeconds != 1320 {
		t.Fatalf("new bests = %+v", got.NewBestEfforts)
	}
}

func TestAggregateRunning_NilPaceWhenNoDistance(t *testing.T) {
	loc := mustLoc(t)
	start := time.Date(2026, 6, 15, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 6, 0, 0, 0, time.UTC)
	got := aggregateRunning(nil, nil, start, end, loc)
	if got == nil {
		t.Fatal("running must be non-nil")
	}
	if got.AvgPaceSecPerKm != nil {
		t.Fatalf("avg pace should be nil with no distance, got %v", *got.AvgPaceSecPerKm)
	}
	if got.Runs == nil || got.NewBestEfforts == nil {
		t.Fatalf("slices must be non-nil: %+v", got)
	}
}

func TestAggregateSteps_DaysLoggedAvgTotalGoal(t *testing.T) {
	loc := mustLoc(t)
	entries := []steps.Entry{
		{Date: "2026-06-17", Steps: 12000},
		{Date: "2026-06-15", Steps: 8000},
		{Date: "2026-06-16", Steps: 0}, // not counted toward days_logged or avg
	}
	got := aggregateSteps(entries, 10000, loc)
	if got.DaysLogged != 2 {
		t.Fatalf("days_logged = %d, want 2", got.DaysLogged)
	}
	if got.Total != 20000 {
		t.Fatalf("total = %d, want 20000", got.Total)
	}
	// avg over the 2 logged days = 20000 / 2 = 10000.
	if got.Avg != 10000 {
		t.Fatalf("avg = %d, want 10000", got.Avg)
	}
	if got.Goal != 10000 {
		t.Fatalf("goal = %d", got.Goal)
	}
	// by_day sorted oldest → newest.
	if len(got.ByDay) != 3 || got.ByDay[0].Date != "2026-06-15" || got.ByDay[2].Date != "2026-06-17" {
		t.Fatalf("by_day = %+v", got.ByDay)
	}
}

func TestAggregateBodyweight_StartEndDelta(t *testing.T) {
	earlier := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	later := time.Date(2026, 6, 20, 7, 0, 0, 0, time.UTC)
	// Provided out of order; aggregation sorts oldest → newest.
	entries := []bodyweightReading{
		{measuredAt: later, weight: 183.4, unit: "lb"},
		{measuredAt: earlier, weight: 184.2, unit: "lb"},
	}
	got := aggregateBodyweight(entries)
	if got.Start != 184.2 || got.End != 183.4 {
		t.Fatalf("start=%v end=%v", got.Start, got.End)
	}
	if got.Delta != -0.8 {
		t.Fatalf("delta = %v, want -0.8", got.Delta)
	}
	if got.Unit != "lb" {
		t.Fatalf("unit = %q", got.Unit)
	}
	if len(got.Readings) != 2 || got.Readings[0].Date != "2026-06-15" || got.Readings[1].Date != "2026-06-20" {
		t.Fatalf("readings = %+v", got.Readings)
	}
}

func TestAggregateBodyweight_EmptyIsNonNil(t *testing.T) {
	got := aggregateBodyweight(nil)
	if got == nil || got.Readings == nil {
		t.Fatalf("empty bodyweight must be non-nil with empty readings: %+v", got)
	}
}

func TestAggregateNutrition_AvgGoalsByDay(t *testing.T) {
	days := []nutrition.DailyMacros{
		{Date: "2026-06-16", Calories: 2700, ProteinG: 200, FatG: 80, CarbsG: 290},
		{Date: "2026-06-15", Calories: 2710.4, ProteinG: 205, FatG: 79, CarbsG: 288},
	}
	goals := nutrition.MacroGoals{Calories: 2700, ProteinG: 200, FatG: 80, CarbsG: 295}
	got := aggregateNutrition(days, goals)
	if got.DaysLogged != 2 {
		t.Fatalf("days_logged = %d, want 2", got.DaysLogged)
	}
	// avg calories = (2700 + 2710.4) / 2 = 2705.2 → rounded 2705.
	if got.Avg.Calories != 2705 || got.Avg.ProteinG != 203 {
		t.Fatalf("avg = %+v", got.Avg)
	}
	if got.Goals != (MacroSet{Calories: 2700, ProteinG: 200, FatG: 80, CarbsG: 295}) {
		t.Fatalf("goals = %+v", got.Goals)
	}
	// by_day sorted oldest → newest, rounded to int.
	if len(got.ByDay) != 2 || got.ByDay[0].Date != "2026-06-15" || got.ByDay[0].Calories != 2710 {
		t.Fatalf("by_day = %+v", got.ByDay)
	}
}

func TestAggregateNutrition_EmptyIsNonNil(t *testing.T) {
	got := aggregateNutrition(nil, nutrition.MacroGoals{})
	if got == nil || got.ByDay == nil {
		t.Fatalf("empty nutrition must be non-nil: %+v", got)
	}
	if got.DaysLogged != 0 || got.Avg != (MacroSet{}) {
		t.Fatalf("empty nutrition = %+v", got)
	}
}

func TestCountActiveDays_UnionOfDomains(t *testing.T) {
	strength := &StrengthSection{Sessions: []StrengthSession{
		{Date: "2026-06-15"}, {Date: "2026-06-17"},
	}}
	running := &RunningSection{Runs: []RunSummary{
		{Date: "2026-06-17"}, // overlaps strength → counted once
		{Date: "2026-06-18"},
	}}
	stepsSec := &StepsSection{ByDay: []StepsDay{
		{Date: "2026-06-15", Steps: 9000}, // overlaps strength
		{Date: "2026-06-19", Steps: 5000},
		{Date: "2026-06-20", Steps: 0}, // zero steps → not active
	}}
	// Union: 15, 17, 18, 19 = 4 distinct days.
	if got := countActiveDays(strength, running, stepsSec); got != 4 {
		t.Fatalf("active days = %d, want 4", got)
	}
}

func TestCountActiveDays_NilSectionsTolerated(t *testing.T) {
	stepsSec := &StepsSection{ByDay: []StepsDay{{Date: "2026-06-15", Steps: 100}}}
	if got := countActiveDays(nil, nil, stepsSec); got != 1 {
		t.Fatalf("active days = %d, want 1", got)
	}
	if got := countActiveDays(nil, nil, nil); got != 0 {
		t.Fatalf("active days = %d, want 0", got)
	}
}
