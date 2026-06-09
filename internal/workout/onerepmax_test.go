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

func TestAggregateOneRepMax_Empty(t *testing.T) {
	entries := AggregateOneRepMax(Workout{ID: "w1", UserID: "u1"})
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for workout with no exercises, got %d", len(entries))
	}
}

func TestAggregateOneRepMax_SingleExercise(t *testing.T) {
	w := Workout{
		ID:          "w1",
		UserID:      "u1",
		PerformedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{{
			ExerciseID: "barbell-bench-press",
			Sets: []Set{
				{Reps: 5, Weight: 185, Unit: user.WeightUnitPounds},
				{Reps: 5, Weight: 195, Unit: user.WeightUnitPounds},
				{Reps: 3, Weight: 205, Unit: user.WeightUnitPounds},
			},
		}},
	}

	entries := AggregateOneRepMax(w)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.UserID != "u1" || e.WorkoutID != "w1" || e.ExerciseID != "barbell-bench-press" {
		t.Errorf("entry identifiers wrong: %+v", e)
	}
	if e.SetCount != 3 {
		t.Errorf("set_count: got %d, want 3", e.SetCount)
	}
	if e.Unit != user.WeightUnitPounds {
		t.Errorf("unit: got %q, want lb", e.Unit)
	}

	// The (195, 5) set's Epley is 227.5 — higher than (205, 3)'s 225.5 —
	// so it's the max despite the lighter weight. Lower reps don't
	// automatically win when the working weight isn't proportionally up.
	wantMin := EpleyOneRM(185, 5)
	wantMax := EpleyOneRM(195, 5)
	wantAvg := (EpleyOneRM(185, 5) + EpleyOneRM(195, 5) + EpleyOneRM(205, 3)) / 3.0
	if math.Abs(e.MinEstimated1RM-wantMin) > 0.0001 {
		t.Errorf("min: got %v, want %v", e.MinEstimated1RM, wantMin)
	}
	if math.Abs(e.MaxEstimated1RM-wantMax) > 0.0001 {
		t.Errorf("max: got %v, want %v", e.MaxEstimated1RM, wantMax)
	}
	if math.Abs(e.AvgEstimated1RM-wantAvg) > 0.0001 {
		t.Errorf("avg: got %v, want %v", e.AvgEstimated1RM, wantAvg)
	}
}

func TestAggregateOneRepMax_MultipleExercises(t *testing.T) {
	w := Workout{
		ID:     "w1",
		UserID: "u1",
		Exercises: []WorkoutExercise{
			{
				ExerciseID: "barbell-bench-press",
				Sets:       []Set{{Reps: 5, Weight: 185, Unit: user.WeightUnitPounds}},
			},
			{
				ExerciseID: "pull-up",
				Sets:       []Set{{Reps: 10, Weight: 0, Unit: user.WeightUnitPounds}},
			},
		},
	}
	entries := AggregateOneRepMax(w)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// First-occurrence order preserved.
	if entries[0].ExerciseID != "barbell-bench-press" || entries[1].ExerciseID != "pull-up" {
		t.Errorf("order not preserved: got %q then %q", entries[0].ExerciseID, entries[1].ExerciseID)
	}
}

func TestAggregateOneRepMax_FoldsDuplicateExerciseBlocks(t *testing.T) {
	// Same exercise listed twice (warmup + main). The aggregation should
	// produce a single entry summarizing both blocks together — same
	// behavior as ComputeProgression in progression.go.
	w := Workout{
		ID:     "w1",
		UserID: "u1",
		Exercises: []WorkoutExercise{
			{
				ExerciseID: "barbell-bench-press",
				Sets:       []Set{{Reps: 8, Weight: 135, Unit: user.WeightUnitPounds}},
			},
			{
				ExerciseID: "barbell-bench-press",
				Sets: []Set{
					{Reps: 5, Weight: 185, Unit: user.WeightUnitPounds},
					{Reps: 3, Weight: 205, Unit: user.WeightUnitPounds},
				},
			},
		},
	}
	entries := AggregateOneRepMax(w)
	if len(entries) != 1 {
		t.Fatalf("expected duplicate-exercise blocks folded into 1 entry, got %d", len(entries))
	}
	if entries[0].SetCount != 3 {
		t.Errorf("set_count after fold: got %d, want 3", entries[0].SetCount)
	}
}

func TestAggregateOneRepMax_DominantUnit_LbWins(t *testing.T) {
	w := Workout{
		ID:     "w1",
		UserID: "u1",
		Exercises: []WorkoutExercise{{
			ExerciseID: "barbell-bench-press",
			Sets: []Set{
				{Reps: 5, Weight: 185, Unit: user.WeightUnitPounds},
				{Reps: 5, Weight: 195, Unit: user.WeightUnitPounds},
				{Reps: 5, Weight: 90, Unit: user.WeightUnitKilograms}, // dropped
			},
		}},
	}
	entries := AggregateOneRepMax(w)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Unit != user.WeightUnitPounds {
		t.Errorf("dominant unit: got %q, want lb", entries[0].Unit)
	}
	if entries[0].SetCount != 2 {
		t.Errorf("set_count after dropping kg set: got %d, want 2", entries[0].SetCount)
	}
}

func TestAggregateOneRepMax_DominantUnit_TieGoesToLb(t *testing.T) {
	// Equal counts → tie-break to lb, matching progression.go's behavior.
	w := Workout{
		ID:     "w1",
		UserID: "u1",
		Exercises: []WorkoutExercise{{
			ExerciseID: "barbell-bench-press",
			Sets: []Set{
				{Reps: 5, Weight: 185, Unit: user.WeightUnitPounds},
				{Reps: 5, Weight: 90, Unit: user.WeightUnitKilograms},
			},
		}},
	}
	entries := AggregateOneRepMax(w)
	if len(entries) != 1 || entries[0].Unit != user.WeightUnitPounds {
		t.Errorf("tie should resolve to lb; got entries=%+v", entries)
	}
}

func TestAggregateOneRepMax_ExerciseWithNoSets_Skipped(t *testing.T) {
	w := Workout{
		ID:     "w1",
		UserID: "u1",
		Exercises: []WorkoutExercise{{
			ExerciseID: "barbell-bench-press",
			Sets:       nil,
		}},
	}
	entries := AggregateOneRepMax(w)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries when exercise has no sets, got %d", len(entries))
	}
}

func TestRecencyWeightedBaseline_Empty(t *testing.T) {
	_, ok := RecencyWeightedBaseline(nil, time.Now(), DefaultBaselineWindow, DefaultBaselineTau)
	if ok {
		t.Error("expected ok=false for empty input")
	}
}

func TestRecencyWeightedBaseline_AllOutsideWindow(t *testing.T) {
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	entries := []OneRepMaxEntry{
		{PerformedAt: at.Add(-200 * 24 * time.Hour), MaxEstimated1RM: 200},
	}
	_, ok := RecencyWeightedBaseline(entries, at, DefaultBaselineWindow, DefaultBaselineTau)
	if ok {
		t.Error("expected ok=false when all entries are older than the window")
	}
}

func TestRecencyWeightedBaseline_SingleEntryReturnsItself(t *testing.T) {
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	entries := []OneRepMaxEntry{
		{PerformedAt: at.Add(-7 * 24 * time.Hour), MaxEstimated1RM: 217.3},
	}
	got, ok := RecencyWeightedBaseline(entries, at, DefaultBaselineWindow, DefaultBaselineTau)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// Weighted average of a single point equals that point's value.
	if math.Abs(got-217.3) > 0.0001 {
		t.Errorf("single-entry baseline: got %v, want 217.3", got)
	}
}

func TestRecencyWeightedBaseline_MoreRecentWeightedMore(t *testing.T) {
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Two entries with equal values produce that value; two entries
	// with different values weighted by recency should produce a result
	// closer to the recent one than a simple average would.
	entries := []OneRepMaxEntry{
		{PerformedAt: at.Add(-3 * 24 * time.Hour), MaxEstimated1RM: 220},  // recent
		{PerformedAt: at.Add(-80 * 24 * time.Hour), MaxEstimated1RM: 180}, // old
	}
	got, ok := RecencyWeightedBaseline(entries, at, DefaultBaselineWindow, DefaultBaselineTau)
	if !ok {
		t.Fatal("expected ok=true")
	}
	simpleAvg := (220.0 + 180.0) / 2
	if !(got > simpleAvg) {
		t.Errorf("recency-weighted result should be closer to 220 than a simple average; got %v, simple=%v", got, simpleAvg)
	}
	// And it should be strictly between the two values.
	if got <= 180 || got >= 220 {
		t.Errorf("baseline must lie between the two values: got %v", got)
	}
}

func TestRecencyWeightedBaseline_DeloadIsBounded(t *testing.T) {
	// The SOW guarantees a one-week deload sitting at 90% of normal
	// drops the baseline by only ~2% — the worked example for τ=45,
	// window=90, twelve weekly entries plus a deload. Reproduce that
	// scenario and assert the baseline stays close to the normal value.
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	normal := 220.0
	deloadValue := normal * 0.9

	entries := []OneRepMaxEntry{
		{PerformedAt: at, MaxEstimated1RM: deloadValue}, // today's deload
	}
	for i := 1; i <= 12; i++ {
		entries = append(entries, OneRepMaxEntry{
			PerformedAt:     at.Add(-time.Duration(i) * 7 * 24 * time.Hour),
			MaxEstimated1RM: normal,
		})
	}

	got, ok := RecencyWeightedBaseline(entries, at, DefaultBaselineWindow, DefaultBaselineTau)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// Baseline should be at most 3% below normal — visible but not crashing.
	dropPct := (normal - got) / normal * 100
	if dropPct < 0 || dropPct > 3 {
		t.Errorf("deload baseline drop: got %.2f%%, want 0-3%%", dropPct)
	}
}

func TestRecencyWeightedBaseline_FutureEntryExcluded(t *testing.T) {
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	entries := []OneRepMaxEntry{
		{PerformedAt: at.Add(24 * time.Hour), MaxEstimated1RM: 999}, // future
		{PerformedAt: at.Add(-1 * time.Hour), MaxEstimated1RM: 200},
	}
	got, _ := RecencyWeightedBaseline(entries, at, DefaultBaselineWindow, DefaultBaselineTau)
	if math.Abs(got-200) > 0.5 {
		t.Errorf("future entries must be excluded; got %v, want ~200", got)
	}
}
