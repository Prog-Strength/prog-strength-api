package workout

import (
	"context"
	"testing"
	"time"
)

// TestGetPersonalRecordEventsByIDs_SQLite seeds PR events by creating a
// workout (first set on an exercise is a PR break) and verifies the batch
// id-keyed read returns exactly the requested events, the empty-input
// contract, and that unknown ids are simply absent.
func TestGetPersonalRecordEventsByIDs_SQLite(t *testing.T) {
	d, _ := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seedExerciseCatalog(t, d, "barbell-bench-press", "back-squat")

	w := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{
			{ExerciseID: "barbell-bench-press", Order: 0, Sets: nSets(2, 5, 185)},
			{ExerciseID: "back-squat", Order: 1, Sets: nSets(1, 3, 315)},
		},
	}
	mustCreate(t, repo, w)
	assertGetPREventsByIDs(t, repo, w.ID)
}

// TestGetPersonalRecordEventsByIDs_Memory mirrors the SQLite test against the
// in-memory repository so both backends are covered.
func TestGetPersonalRecordEventsByIDs_Memory(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()

	w := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{
			{ExerciseID: "barbell-bench-press", Order: 0, Sets: nSets(2, 5, 185)},
			{ExerciseID: "back-squat", Order: 1, Sets: nSets(1, 3, 315)},
		},
	}
	if err := repo.Create(ctx, w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertGetPREventsByIDs(t, repo, w.ID)
}

// assertGetPREventsByIDs is the shared body run against each backend.
func assertGetPREventsByIDs(t *testing.T, repo Repository, workoutID string) {
	t.Helper()
	ctx := context.Background()

	// The two exercises each produce a first-time PR break event.
	all, err := repo.ListPersonalRecordEventsByWorkouts(ctx, []string{workoutID})
	if err != nil {
		t.Fatalf("ListPersonalRecordEventsByWorkouts: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("seeded %d PR events, want 2", len(all))
	}
	ids := []string{all[0].ID, all[1].ID}

	// Empty input → empty result, no error.
	empty, err := repo.GetPersonalRecordEventsByIDs(ctx, nil)
	if err != nil {
		t.Fatalf("GetPersonalRecordEventsByIDs(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty input returned %d events, want 0", len(empty))
	}

	// Both ids → both events.
	got, err := repo.GetPersonalRecordEventsByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("GetPersonalRecordEventsByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	gotIDs := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !gotIDs[ids[0]] || !gotIDs[ids[1]] {
		t.Errorf("returned ids %v, want %v", gotIDs, ids)
	}

	// One real id + one unknown id → only the real event.
	mixed, err := repo.GetPersonalRecordEventsByIDs(ctx, []string{ids[0], "does-not-exist"})
	if err != nil {
		t.Fatalf("GetPersonalRecordEventsByIDs(mixed): %v", err)
	}
	if len(mixed) != 1 || mixed[0].ID != ids[0] {
		t.Errorf("mixed lookup = %v, want [%s]", mixed, ids[0])
	}
}
