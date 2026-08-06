package strength

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

func TestDescriptor_ValidateCreate(t *testing.T) {
	details := func(s string) json.RawMessage { return json.RawMessage(s) }

	cases := []struct {
		name    string
		details json.RawMessage
		wantErr error // nil = expect success; sentinel = errors.Is match; use errAny for "any error"
	}{
		{"valid exercises", details(`{"exercises":[{"exercise_id":"back-squat","sets":[{"reps":5,"weight":225,"unit":"lb"}]}]}`), nil},
		{"absent details is a zero-exercise lift", nil, nil},
		{"null details is a zero-exercise lift", details(`null`), nil},
		{"empty exercises list", details(`{"exercises":[]}`), nil},
		{"missing exercise id", details(`{"exercises":[{"sets":[{"reps":5,"weight":225,"unit":"lb"}]}]}`), ErrExerciseIDRequired},
		{"exercise without sets", details(`{"exercises":[{"exercise_id":"back-squat","sets":[]}]}`), ErrSetsRequired},
		{"zero reps", details(`{"exercises":[{"exercise_id":"back-squat","sets":[{"reps":0,"weight":225,"unit":"lb"}]}]}`), ErrInvalidReps},
		{"negative weight", details(`{"exercises":[{"exercise_id":"back-squat","sets":[{"reps":5,"weight":-1,"unit":"lb"}]}]}`), ErrInvalidWeight},
		{"malformed json", details(`{`), errAny},
		{"bad unit", details(`{"exercises":[{"exercise_id":"back-squat","sets":[{"reps":5,"weight":225,"unit":"stone"}]}]}`), errAny},
	}
	d := NewDescriptor(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := d.ValidateCreate(activity.CreateRequest{
				Type:      activity.ActivityStrengthTraining,
				StartTime: time.Now(),
				Details:   tc.details,
			})
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("ValidateCreate = %v, want nil", err)
			case errors.Is(tc.wantErr, errAny) && err == nil:
				t.Fatal("ValidateCreate = nil, want error")
			case tc.wantErr != nil && !errors.Is(tc.wantErr, errAny) && !errors.Is(err, tc.wantErr):
				t.Fatalf("ValidateCreate = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// errAny marks table cases where any non-nil error passes (wrapped decode /
// enum errors without a package sentinel).
var errAny = errors.New("any error")

func TestDescriptor_SummarizeMirrorsWorkoutCard(t *testing.T) {
	d := NewDescriptor(nil)
	name := "Leg Day"
	a := activity.Activity{Name: &name, ActivityType: activity.ActivityStrengthTraining}
	exercises := []WorkoutExercise{
		{ExerciseID: "back-squat", Sets: nSets(2, 5, 100)},
		{ExerciseID: "leg-press", Sets: nSets(2, 5, 100)},
	}

	got := d.Summarize(a, &Details{Exercises: exercises})
	want := activity.Summary{
		Title:    "Leg Day",
		Subtitle: "2 exercises",
		Metrics:  []string{"2 exercises", "4 sets", "2,000 lb"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Summarize = %+v, want %+v", got, want)
	}
}

func TestDescriptor_SummarizeDefaultsAndOmitsEmptyChips(t *testing.T) {
	d := NewDescriptor(nil)

	// Nameless, zero-exercise lift: title falls back, no sets/volume chips
	// (mirrors the hydrator, which only appends non-zero chips).
	got := d.Summarize(activity.Activity{}, nil)
	want := activity.Summary{
		Title:    "Workout",
		Subtitle: "0 exercises",
		Metrics:  []string{"0 exercises"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Summarize(empty) = %+v, want %+v", got, want)
	}

	// Bodyweight-only session: sets chip present, volume chip omitted.
	exercises := []WorkoutExercise{{ExerciseID: "pull-up", Sets: []Set{{Reps: 10, Weight: 0, Unit: user.WeightUnitPounds}}}}
	got = d.Summarize(activity.Activity{}, &Details{Exercises: exercises})
	want = activity.Summary{
		Title:    "Workout",
		Subtitle: "1 exercise",
		Metrics:  []string{"1 exercise", "1 set"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Summarize(bodyweight) = %+v, want %+v", got, want)
	}
}

func TestDescriptor_DetailStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	dbConn, _ := newTestDB(t)
	repo := NewSQLiteRepository(dbConn)
	seedExerciseCatalog(t, dbConn, "back-squat", "leg-press")
	d := NewDescriptor(repo)

	w := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		Exercises:   []WorkoutExercise{{ExerciseID: "back-squat", Order: 0, Sets: nSets(3, 5, 225)}},
	}
	mustCreate(t, repo, w)

	loaded, err := d.Details.Load(ctx, "u1", w.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	det, ok := loaded.(*Details)
	if !ok {
		t.Fatalf("Load returned %T, want *Details", loaded)
	}
	if len(det.Exercises) != 1 || det.Exercises[0].ExerciseID != "back-squat" || len(det.Exercises[0].Sets) != 3 {
		t.Fatalf("Load = %+v, want the created exercise with 3 sets", det.Exercises)
	}

	// Save replaces the exercise list wholesale (full-replacement semantics,
	// same as the unified PUT /activities update path).
	replacement := &Details{Exercises: []WorkoutExercise{{ExerciseID: "leg-press", Order: 0, Sets: nSets(2, 8, 180)}}}
	if saveErr := d.Details.Save(ctx, "u1", w.ID, replacement); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	loaded, err = d.Details.Load(ctx, "u1", w.ID)
	if err != nil {
		t.Fatalf("Load after save: %v", err)
	}
	det = loaded.(*Details)
	if len(det.Exercises) != 1 || det.Exercises[0].ExerciseID != "leg-press" {
		t.Fatalf("Load after save = %+v, want the replacement exercise", det.Exercises)
	}

	// Delete clears the children; the base row (and its zero-exercise
	// workout view) survives — soft-deleting the base is the base
	// domain's job, not the detail store's.
	if delErr := d.Details.Delete(ctx, "u1", w.ID); delErr != nil {
		t.Fatalf("Delete: %v", delErr)
	}
	loaded, err = d.Details.Load(ctx, "u1", w.ID)
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if det = loaded.(*Details); len(det.Exercises) != 0 {
		t.Fatalf("Load after delete = %+v, want no exercises", det.Exercises)
	}
}

func TestDescriptor_DetailStoreEnforcesOwnership(t *testing.T) {
	ctx := context.Background()
	dbConn, _ := newTestDB(t)
	repo := NewSQLiteRepository(dbConn)
	seedExerciseCatalog(t, dbConn, "back-squat")
	d := NewDescriptor(repo)

	w := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		Exercises:   []WorkoutExercise{{ExerciseID: "back-squat", Order: 0, Sets: nSets(3, 5, 225)}},
	}
	mustCreate(t, repo, w)

	// The DetailStore contract sentinel is the BASE package's
	// activity.ErrNotFound for every implementation, so the unified
	// handler's error mapping never branches per type.
	if _, err := d.Details.Load(ctx, "intruder", w.ID); !errors.Is(err, activity.ErrNotFound) {
		t.Fatalf("Load as wrong user err = %v, want activity.ErrNotFound", err)
	}
	if err := d.Details.Save(ctx, "intruder", w.ID, &Details{}); !errors.Is(err, activity.ErrNotFound) {
		t.Fatalf("Save as wrong user err = %v, want activity.ErrNotFound", err)
	}
	if err := d.Details.Delete(ctx, "intruder", w.ID); !errors.Is(err, activity.ErrNotFound) {
		t.Fatalf("Delete as wrong user err = %v, want activity.ErrNotFound", err)
	}
	// A missing activity id reads the same way.
	if _, err := d.Details.Load(ctx, "u1", "no-such-activity"); !errors.Is(err, activity.ErrNotFound) {
		t.Fatalf("Load of missing id err = %v, want activity.ErrNotFound", err)
	}
	// Owner's exercises untouched by the intruder's attempts.
	loaded, err := d.Details.Load(ctx, "u1", w.ID)
	if err != nil {
		t.Fatalf("Load as owner: %v", err)
	}
	if det := loaded.(*Details); len(det.Exercises) != 1 {
		t.Fatalf("owner exercises = %+v, want the original one", det.Exercises)
	}
}
