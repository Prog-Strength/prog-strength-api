package workout

import (
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Default headline exercise slugs must exist in the catalog — a typo
// here would otherwise ship silently and a new user's PR page would
// have a phantom placeholder card for an exercise that doesn't exist.
func TestHeadlineExercises_AllInCatalog(t *testing.T) {
	catalog := map[string]bool{}
	for _, e := range exercise.Catalog {
		catalog[e.ID] = true
	}
	for _, slug := range HeadlineExercises {
		if !catalog[slug] {
			t.Errorf("default headline exercise %q is not in the exercise catalog", slug)
		}
	}
}

// Helper: build a workout snapshot from (id, t, exerciseID, sets).
func snap(id string, t time.Time, exID string, sets ...Set) WorkoutSnapshot {
	return WorkoutSnapshot{
		ID:          id,
		UserID:      "u1",
		PerformedAt: t,
		Exercises:   []ExerciseSnapshot{{ExerciseID: exID, Sets: sets}},
	}
}
func set(reps int, weight float64) Set {
	return Set{Reps: reps, Weight: weight, Unit: user.WeightUnitPounds}
}

func TestRecomputePR_Empty(t *testing.T) {
	pr, events := RecomputePersonalRecord(nil, "barbell-bench-press")
	if pr != nil {
		t.Errorf("expected nil PR for empty input, got %+v", pr)
	}
	if len(events) != 0 {
		t.Errorf("expected no events for empty input, got %d", len(events))
	}
}

func TestRecomputePR_NoMatchingExercise(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []WorkoutSnapshot{
		snap("w1", day, "barbell-bench-press", set(5, 185)),
	}
	pr, events := RecomputePersonalRecord(snaps, "barbell-deadlift")
	if pr != nil {
		t.Errorf("expected nil PR when no workout has the exercise, got %+v", pr)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d", len(events))
	}
}

func TestRecomputePR_FirstSetIsPR(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []WorkoutSnapshot{
		snap("w1", day, "barbell-bench-press", set(5, 185)),
	}
	pr, events := RecomputePersonalRecord(snaps, "barbell-bench-press")
	if pr == nil {
		t.Fatal("expected a PR, got nil")
	}
	if pr.Weight != 185 || pr.Reps != 5 {
		t.Errorf("PR weight/reps: got %v×%d, want 185×5", pr.Weight, pr.Reps)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event for first PR, got %d", len(events))
	}
	if events[0].PreviousWeight != nil {
		t.Error("first PR event should have no previous_weight")
	}
}

func TestRecomputePR_AscendingChainProducesMultipleEvents(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []WorkoutSnapshot{
		snap("w1", start, "barbell-bench-press", set(1, 200)),
		snap("w2", start.AddDate(0, 0, 7), "barbell-bench-press", set(1, 210)),
		snap("w3", start.AddDate(0, 0, 14), "barbell-bench-press", set(3, 215)),
	}
	pr, events := RecomputePersonalRecord(snaps, "barbell-bench-press")
	if pr == nil || pr.Weight != 215 {
		t.Fatalf("final PR: got %+v, want 215", pr)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events (one per PR break), got %d", len(events))
	}
	// Second event's previous should reference the first PR.
	if events[1].PreviousWeight == nil || *events[1].PreviousWeight != 200 {
		t.Errorf("event[1].previous_weight: got %v, want 200", events[1].PreviousWeight)
	}
}

func TestRecomputePR_NonImprovingWorkoutsSkipped(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []WorkoutSnapshot{
		snap("w1", start, "barbell-bench-press", set(1, 200)),
		snap("w2", start.AddDate(0, 0, 7), "barbell-bench-press", set(5, 185)),  // lighter
		snap("w3", start.AddDate(0, 0, 14), "barbell-bench-press", set(1, 200)), // tie
	}
	pr, events := RecomputePersonalRecord(snaps, "barbell-bench-press")
	if pr == nil || pr.WorkoutID != "w1" {
		t.Fatalf("PR should still belong to w1, got %+v", pr)
	}
	if len(events) != 1 {
		t.Errorf("only the first set should produce an event, got %d", len(events))
	}
}

func TestRecomputePR_HeaviestSetPerWorkoutWins(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Ramping up within a single workout: 295, 300, 305. The recompute
	// should emit one event for the heaviest set (305), not three.
	snaps := []WorkoutSnapshot{
		snap("w1", day, "barbell-bench-press",
			set(1, 295),
			set(1, 300),
			set(1, 305),
		),
	}
	pr, events := RecomputePersonalRecord(snaps, "barbell-bench-press")
	if pr == nil || pr.Weight != 305 {
		t.Fatalf("PR: got %+v, want 305", pr)
	}
	if len(events) != 1 {
		t.Errorf("intra-workout ramping should collapse to 1 event, got %d", len(events))
	}
}

func TestRecomputePR_DuplicateExerciseBlocksFolded(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Same exercise appears in two blocks within one workout (e.g.
	// warmup + main). The heaviest across both blocks should win.
	snaps := []WorkoutSnapshot{{
		ID:          "w1",
		UserID:      "u1",
		PerformedAt: day,
		Exercises: []ExerciseSnapshot{
			{ExerciseID: "barbell-bench-press", Sets: []Set{set(8, 135)}},
			{ExerciseID: "barbell-bench-press", Sets: []Set{set(1, 225)}},
		},
	}}
	pr, _ := RecomputePersonalRecord(snaps, "barbell-bench-press")
	if pr == nil || pr.Weight != 225 {
		t.Errorf("folded heaviest: got %+v, want 225", pr)
	}
}

func TestRecomputePR_MixedUnits_ConvertOnCompare(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 100 kg ≈ 220.46 lb; should beat a prior 200 lb PR.
	snaps := []WorkoutSnapshot{
		snap("w1", start, "barbell-bench-press", set(1, 200)),
		{
			ID:          "w2",
			UserID:      "u1",
			PerformedAt: start.AddDate(0, 0, 7),
			Exercises: []ExerciseSnapshot{{
				ExerciseID: "barbell-bench-press",
				Sets:       []Set{{Reps: 1, Weight: 100, Unit: user.WeightUnitKilograms}},
			}},
		},
	}
	pr, events := RecomputePersonalRecord(snaps, "barbell-bench-press")
	if pr == nil {
		t.Fatal("expected a PR")
	}
	if pr.Unit != user.WeightUnitKilograms || pr.Weight != 100 {
		t.Errorf("PR should be stored in the candidate's original unit; got %v %v", pr.Weight, pr.Unit)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events (initial + mixed-unit break), got %d", len(events))
	}
}

func TestRecomputePR_BodyweightSetsProduceNoPR(t *testing.T) {
	// A workout containing only bodyweight sets (weight = 0) must not
	// produce a PR. The personal_records schema's CHECK(weight > 0)
	// rejected these in production and crashed the backfill; this
	// test pins the recompute-side filter so the constraint can't be
	// violated again.
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []WorkoutSnapshot{
		snap("w1", day, "pull-up",
			Set{Reps: 10, Weight: 0, Unit: user.WeightUnitPounds},
			Set{Reps: 8, Weight: 0, Unit: user.WeightUnitPounds},
		),
	}
	pr, events := RecomputePersonalRecord(snaps, "pull-up")
	if pr != nil {
		t.Errorf("bodyweight-only workout should not produce a PR, got %+v", pr)
	}
	if len(events) != 0 {
		t.Errorf("bodyweight-only workout should not produce events, got %d", len(events))
	}
}

func TestRecomputePR_MixedBodyweightAndWeightedKeepsLoaded(t *testing.T) {
	// Weighted pull-up scenario: the workout has a bodyweight warmup
	// at 0 plus a working set at +25 lb. The PR should be the +25 set,
	// the bodyweight set ignored — not dragged in as a candidate.
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []WorkoutSnapshot{
		snap("w1", day, "weighted-pull-up",
			Set{Reps: 5, Weight: 0, Unit: user.WeightUnitPounds},
			Set{Reps: 5, Weight: 25, Unit: user.WeightUnitPounds},
		),
	}
	pr, _ := RecomputePersonalRecord(snaps, "weighted-pull-up")
	if pr == nil || pr.Weight != 25 {
		t.Errorf("expected PR at the loaded set (25 lb), got %+v", pr)
	}
}

func TestRecomputePR_NoIntraWorkoutImprovementYieldsOneEvent(t *testing.T) {
	// Edge case: within a workout, the heaviest set ties the existing
	// PR. No event should fire.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snaps := []WorkoutSnapshot{
		snap("w1", start, "barbell-bench-press", set(1, 300)),
		snap("w2", start.AddDate(0, 0, 7), "barbell-bench-press", set(5, 300)),
	}
	pr, events := RecomputePersonalRecord(snaps, "barbell-bench-press")
	if pr.WorkoutID != "w1" {
		t.Errorf("ties should not overwrite; PR should still belong to w1, got %v", pr.WorkoutID)
	}
	if len(events) != 1 {
		t.Errorf("tie should produce no new event; got %d total events", len(events))
	}
}
