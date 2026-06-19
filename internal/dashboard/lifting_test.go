package dashboard

import (
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// sess builds a workout with one exercise carrying the given sets, performed
// at start and ending durSec later (durSec < 0 means no EndedAt).
func sess(start time.Time, durSec int, sets ...workout.Set) workout.Workout {
	w := workout.Workout{
		PerformedAt: start,
		Exercises:   []workout.WorkoutExercise{{Sets: sets}},
	}
	if durSec >= 0 {
		end := start.Add(time.Duration(durSec) * time.Second)
		w.EndedAt = &end
	}
	return w
}

func set(weight float64, reps int) workout.Set {
	return workout.Set{Weight: weight, Reps: reps, Unit: user.WeightUnitPounds}
}

func TestBuildLifting_EmptyReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	if got := buildLifting(nil, 0, nil, "lb", now, denver); got != nil {
		t.Errorf("no workouts should be nil, got %+v", got)
	}
}

func TestBuildLifting_CurrentWeek(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver) // current week Mon = 06-15

	headline := &Headline1RM{ExerciseName: "Barbell Bench Press", Value: 326.9, Unit: "lb"}
	workouts := []workout.Workout{
		// In current week: 3 sets + 2 sets, 3600s.
		sess(time.Date(2026, 6, 15, 17, 0, 0, 0, denver), 3600, set(100, 5), set(100, 5), set(100, 5)),
		sess(time.Date(2026, 6, 16, 17, 0, 0, 0, denver), 1800, set(50, 8), set(50, 8)),
		// In current week but no EndedAt: counts as session+sets, no duration.
		sess(time.Date(2026, 6, 17, 6, 0, 0, 0, denver), -1, set(60, 10)),
		// Prior week: excluded from current_week rollup.
		sess(time.Date(2026, 6, 8, 17, 0, 0, 0, denver), 1000, set(40, 5)),
	}

	got := buildLifting(workouts, 4, headline, "lb", now, denver)
	if got == nil {
		t.Fatal("expected section")
	}
	if got.CurrentWeek.Sessions != 3 {
		t.Errorf("sessions = %d, want 3", got.CurrentWeek.Sessions)
	}
	if got.CurrentWeek.Sets != 6 {
		t.Errorf("sets = %d, want 6", got.CurrentWeek.Sets)
	}
	if got.CurrentWeek.DurationSeconds != 5400 {
		t.Errorf("duration = %d, want 5400", got.CurrentWeek.DurationSeconds)
	}
	if got.CurrentWeek.PRs != 4 {
		t.Errorf("prs = %d, want 4", got.CurrentWeek.PRs)
	}
	if got.Unit != "lb" {
		t.Errorf("unit = %q, want lb", got.Unit)
	}
	if got.HeadlineEstimated1RM != headline {
		t.Errorf("headline mismatch: %+v", got.HeadlineEstimated1RM)
	}
}

func TestBuildLifting_NilHeadline(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	w := sess(time.Date(2026, 6, 16, 17, 0, 0, 0, denver), 600, set(100, 5))
	got := buildLifting([]workout.Workout{w}, 0, nil, "kg", now, denver)
	if got.HeadlineEstimated1RM != nil {
		t.Errorf("nil headline should pass through, got %+v", got.HeadlineEstimated1RM)
	}
}

func TestBuildLifting_WeeklyVolumeSparkZeroFilledAndLocalBucketing(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// Buckets (Mondays): 04-27,05-04,05-11,05-18,05-25,06-01,06-08,06-15

	workouts := []workout.Workout{
		// 06-15 week: 100*5 + 100*5 = 1000; plus 50*8=400 -> 1400.
		sess(time.Date(2026, 6, 15, 17, 0, 0, 0, denver), 600, set(100, 5), set(100, 5)),
		sess(time.Date(2026, 6, 17, 6, 0, 0, 0, denver), 600, set(50, 8)),
		// 06-01 week: 40*10 = 400.
		sess(time.Date(2026, 6, 2, 17, 0, 0, 0, denver), 600, set(40, 10)),
		// Too old: ignored.
		sess(time.Date(2026, 4, 1, 17, 0, 0, 0, denver), 600, set(99, 9)),
		// 2026-06-15 05:00 UTC = Sun 23:00 Denver -> 06-08 Denver week.
		sess(time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC), 600, set(10, 10)),
	}

	got := buildLifting(workouts, 0, nil, "lb", now, denver)
	want := []float64{0, 0, 0, 0, 0, 400, 100, 1400}
	if !reflect.DeepEqual(got.WeeklyVolumeSpark, want) {
		t.Errorf("spark = %v, want %v", got.WeeklyVolumeSpark, want)
	}
	if len(got.WeeklyVolumeSpark) != sparkWeeks {
		t.Errorf("spark length = %d, want %d", len(got.WeeklyVolumeSpark), sparkWeeks)
	}
}
