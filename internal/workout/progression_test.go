package workout

import (
	"math"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

func TestEpleyOneRM(t *testing.T) {
	tests := []struct {
		name   string
		weight float64
		reps   int
		want   float64
	}{
		{"single rep is the lift itself", 225, 1, 225},
		{"zero reps clamped to weight", 225, 0, 225},
		{"five reps at 185", 185, 5, 185 * (1 + 5.0/30.0)},
		{"ten reps at 135", 135, 10, 135 * (1 + 10.0/30.0)},
		{"bodyweight (weight=0) stays 0", 0, 8, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EpleyOneRM(tc.weight, tc.reps)
			if math.Abs(got-tc.want) > 0.001 {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestComputeProgression_Empty(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	result := ComputeProgression(nil, "barbell-bench-press", since, until)

	if result.ExerciseID != "barbell-bench-press" {
		t.Errorf("exercise_id: got %q, want %q", result.ExerciseID, "barbell-bench-press")
	}
	if len(result.Points) != 0 {
		t.Errorf("expected 0 points, got %d", len(result.Points))
	}
	if result.TrendlineAvg != nil || result.TrendlineMax != nil || result.TrendlineMin != nil {
		t.Error("expected nil trendlines when no points")
	}
	if result.Unit != "" {
		t.Errorf("expected empty unit, got %q", result.Unit)
	}
}

func TestComputeProgression_SingleWorkout_NoTrendline(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	w := Workout{
		ID:          "w1",
		PerformedAt: time.Date(2026, 2, 15, 8, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{{
			ExerciseID: "barbell-bench-press",
			Sets: []Set{
				{Reps: 5, Weight: 185, Unit: user.WeightUnitPounds},
				{Reps: 5, Weight: 185, Unit: user.WeightUnitPounds},
				{Reps: 3, Weight: 205, Unit: user.WeightUnitPounds},
			},
		}},
	}

	result := ComputeProgression([]Workout{w}, "barbell-bench-press", since, until)

	if len(result.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(result.Points))
	}
	if result.Points[0].SetCount != 3 {
		t.Errorf("set_count: got %d, want 3", result.Points[0].SetCount)
	}
	// 1 point can't form a regression line.
	if result.TrendlineAvg != nil {
		t.Error("expected nil trendline for single point")
	}
	if result.Unit != "lb" {
		t.Errorf("unit: got %q, want lb", result.Unit)
	}
}

func TestComputeProgression_AscendingTrend(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Three workouts a month apart, each 1RM estimate clearly going up.
	workouts := []Workout{
		mkWorkout("w1", time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC), 5, 185),
		mkWorkout("w2", time.Date(2026, 2, 15, 8, 0, 0, 0, time.UTC), 5, 195),
		mkWorkout("w3", time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC), 5, 205),
	}

	result := ComputeProgression(workouts, "barbell-bench-press", since, until)

	if len(result.Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(result.Points))
	}
	if result.TrendlineAvg == nil {
		t.Fatal("expected non-nil trendline for 3+ points")
	}
	// Ascending series → end value should be strictly greater than start.
	if result.TrendlineAvg.EndValue <= result.TrendlineAvg.StartValue {
		t.Errorf(
			"expected positive slope: start=%v end=%v",
			result.TrendlineAvg.StartValue, result.TrendlineAvg.EndValue,
		)
	}
}

func TestComputeProgression_MixedUnits_DominantWins(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Three workouts with mostly-lb sets and one stray kg set in the
	// last workout. Dominant unit is lb (5 lb sets vs 1 kg set); the
	// kg set should be excluded and counted in SkippedOtherUnitCount.
	workouts := []Workout{
		mkWorkout("w1", time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC), 5, 185),
		mkWorkout("w2", time.Date(2026, 2, 15, 8, 0, 0, 0, time.UTC), 5, 195),
		{
			ID:          "w3",
			PerformedAt: time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC),
			Exercises: []WorkoutExercise{{
				ExerciseID: "barbell-bench-press",
				Sets: []Set{
					{Reps: 5, Weight: 205, Unit: user.WeightUnitPounds},
					{Reps: 5, Weight: 100, Unit: user.WeightUnitKilograms}, // ← oddball
				},
			}},
		},
	}

	result := ComputeProgression(workouts, "barbell-bench-press", since, until)

	if result.Unit != "lb" {
		t.Errorf("dominant unit: got %q, want lb", result.Unit)
	}
	if result.SkippedOtherUnitCount != 1 {
		t.Errorf("skipped: got %d, want 1", result.SkippedOtherUnitCount)
	}
	// w3's lb set should still contribute a point — only the kg set
	// was dropped, not the whole workout.
	if len(result.Points) != 3 {
		t.Errorf("expected 3 points, got %d", len(result.Points))
	}
}

func TestComputeProgression_SameDayWorkouts_NoTrendline(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	day := time.Date(2026, 2, 15, 8, 0, 0, 0, time.UTC)

	// Multiple workouts on the same instant → zero X-variance → the
	// regression denominator is 0 and the trendline must be nil.
	workouts := []Workout{
		mkWorkout("w1", day, 5, 185),
		mkWorkout("w2", day, 5, 195),
	}

	result := ComputeProgression(workouts, "barbell-bench-press", since, until)

	if len(result.Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(result.Points))
	}
	if result.TrendlineAvg != nil {
		t.Error("expected nil trendline when all points share the same X")
	}
}

// mkWorkout builds a minimal Workout with one exercise and one set —
// reduces the boilerplate in the trend-shape tests where the precise
// shape of the workout doesn't matter, just the timestamp and load.
func mkWorkout(id string, at time.Time, reps int, weight float64) Workout {
	return Workout{
		ID:          id,
		PerformedAt: at,
		Exercises: []WorkoutExercise{{
			ExerciseID: "barbell-bench-press",
			Sets: []Set{
				{Reps: reps, Weight: weight, Unit: user.WeightUnitPounds},
			},
		}},
	}
}
