package plannedworkout

import (
	"context"
	"testing"
	"time"
)

func TestMemoryRepository_Contract(t *testing.T) {
	runRepositoryContract(t, func(t *testing.T) Repository {
		return NewMemoryRepository()
	})
}

// TestMemoryRepository_DeepCopyIsolation asserts callers can't mutate the
// repo's internal state through the slices/pointers they read back.
func TestMemoryRepository_DeepCopyIsolation(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	pw := newPlan("u1", time.Date(2026, 6, 20, 17, 0, 0, 0, time.UTC))
	pw.Exercises = []PlannedExercise{
		{ExerciseID: "bench", Sets: []PlannedSet{{TargetReps: ptrInt(5), TargetWeight: ptrF(185), Unit: ptrStr("lb")}}},
	}
	if err := repo.Create(ctx, pw); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Mutate the caller-held struct after Create; the store must not change.
	pw.Exercises[0].ExerciseID = "TAMPERED"
	*pw.Exercises[0].Sets[0].TargetReps = 999

	got, err := repo.Get(ctx, "u1", pw.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Exercises[0].ExerciseID != "bench" {
		t.Errorf("store mutated through input slice: %q", got.Exercises[0].ExerciseID)
	}
	if *got.Exercises[0].Sets[0].TargetReps != 5 {
		t.Errorf("store mutated through input pointer: %d", *got.Exercises[0].Sets[0].TargetReps)
	}

	// Mutate the returned copy; a second Get must still be pristine.
	got.Exercises[0].ExerciseID = "ALSO_TAMPERED"
	*got.Exercises[0].Sets[0].TargetReps = -1
	again, err := repo.Get(ctx, "u1", pw.ID)
	if err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if again.Exercises[0].ExerciseID != "bench" || *again.Exercises[0].Sets[0].TargetReps != 5 {
		t.Errorf("store mutated through returned copy: %+v", again.Exercises[0])
	}
}
