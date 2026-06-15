package server

import (
	"context"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// --- fakes -------------------------------------------------------------

// fakeWorkoutRepo embeds workout.Repository (so unused methods panic) and
// implements only the reads the hydrator touches. It counts calls so the
// test can assert PR hydration is a single batch query (no N+1).
type fakeWorkoutRepo struct {
	workout.Repository
	workouts        map[string]*workout.Workout
	prEvents        map[string]workout.PersonalRecordEvent
	getByIDCalls    int
	getPREventCalls int
}

func (f *fakeWorkoutRepo) GetByID(_ context.Context, id string) (*workout.Workout, error) {
	f.getByIDCalls++
	w, ok := f.workouts[id]
	if !ok {
		return nil, workout.ErrNotFound
	}
	return w, nil
}

func (f *fakeWorkoutRepo) GetPersonalRecordEventsByIDs(_ context.Context, ids []string) ([]workout.PersonalRecordEvent, error) {
	f.getPREventCalls++
	var out []workout.PersonalRecordEvent
	for _, id := range ids {
		if e, ok := f.prEvents[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// fakeActivityRepo embeds activity.Repository and implements only Get.
type fakeActivityRepo struct {
	activity.Repository
	activities map[string]*activity.Activity
	getCalls   int
}

func (f *fakeActivityRepo) Get(_ context.Context, userID, id string) (*activity.Activity, error) {
	f.getCalls++
	a, ok := f.activities[id]
	if !ok || a.UserID != userID {
		return nil, activity.ErrNotFound
	}
	return a, nil
}

func strptr(s string) *string { return &s }

// --- tests -------------------------------------------------------------

func TestHydrate_PerSourceContent(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	wRepo := &fakeWorkoutRepo{
		workouts: map[string]*workout.Workout{
			"w1": {
				ID:     "w1",
				UserID: "u1",
				Name:   "Push day",
				Exercises: []workout.WorkoutExercise{
					{ExerciseID: "bench", Sets: []workout.Set{
						{Reps: 5, Weight: 100, Unit: user.WeightUnitPounds},
						{Reps: 5, Weight: 100, Unit: user.WeightUnitPounds},
					}},
					{ExerciseID: "ohp", Sets: []workout.Set{
						{Reps: 8, Weight: 50, Unit: user.WeightUnitPounds},
					}},
				},
			},
		},
		prEvents: map[string]workout.PersonalRecordEvent{
			"pr1": {ID: "pr1", UserID: "u1", ExerciseID: "bench", Weight: 305, Reps: 3, Unit: user.WeightUnitPounds, AchievedAt: now},
		},
	}
	aRepo := &fakeActivityRepo{
		activities: map[string]*activity.Activity{
			"a1": {
				ID:              "a1",
				UserID:          "u1",
				ActivityType:    activity.ActivityRunning,
				Name:            strptr("Morning run"),
				DistanceMeters:  8046.72, // 5.0 mi
				DurationSeconds: 2472,    // 41:12
				BestEfforts: []activity.ActivityBestEffort{
					{DistanceKey: "5k", DurationSeconds: 1530}, // 25:30
				},
			},
		},
	}

	h := newTimelineHydrator(wRepo, aRepo)

	refs := []timeline.PostRef{
		{UserID: "u1", SourceType: timeline.SourceWorkout, SourceID: "w1", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourceRun, SourceID: "a1", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourcePR, SourceID: "pr1", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourceBestEffort, SourceID: "a1:5k", OccurredAt: now},
	}

	got, err := h.Hydrate(context.Background(), refs)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d contents, want 4", len(got))
	}

	// workout
	wc := got[refs[0]]
	if wc.Title != "Push day" {
		t.Errorf("workout title = %q, want Push day", wc.Title)
	}
	if wc.Metrics[0] != "2 exercises" {
		t.Errorf("workout metrics[0] = %q, want 2 exercises", wc.Metrics[0])
	}
	if wc.Href != "/activities?view=workouts" {
		t.Errorf("workout href = %q", wc.Href)
	}

	// run — match the SOW example chips 5.0 mi · 41:12
	rc := got[refs[1]]
	if rc.Title != "Morning run" {
		t.Errorf("run title = %q", rc.Title)
	}
	if len(rc.Metrics) != 2 || rc.Metrics[0] != "5.0 mi" || rc.Metrics[1] != "41:12" {
		t.Errorf("run metrics = %v, want [5.0 mi 41:12]", rc.Metrics)
	}
	if rc.Href != "/activities?view=running" {
		t.Errorf("run href = %q", rc.Href)
	}

	// pr
	pc := got[refs[2]]
	if pc.Title != "bench PR" {
		t.Errorf("pr title = %q, want bench PR", pc.Title)
	}
	if pc.Metrics[0] != "305 lb × 3" {
		t.Errorf("pr metrics[0] = %q, want 305 lb × 3", pc.Metrics[0])
	}
	if pc.Href != "/personal-records" {
		t.Errorf("pr href = %q", pc.Href)
	}

	// best_effort
	bc := got[refs[3]]
	if bc.Title != "5K best effort" {
		t.Errorf("best_effort title = %q, want 5K best effort", bc.Title)
	}
	if bc.Metrics[0] != "25:30" {
		t.Errorf("best_effort metrics[0] = %q, want 25:30", bc.Metrics[0])
	}
	if bc.Href != "/activities?view=running" {
		t.Errorf("best_effort href = %q", bc.Href)
	}
}

func TestHydrate_OmitsMissingSources(t *testing.T) {
	now := time.Now().UTC()
	wRepo := &fakeWorkoutRepo{workouts: map[string]*workout.Workout{}, prEvents: map[string]workout.PersonalRecordEvent{}}
	aRepo := &fakeActivityRepo{activities: map[string]*activity.Activity{}}
	h := newTimelineHydrator(wRepo, aRepo)

	refs := []timeline.PostRef{
		{UserID: "u1", SourceType: timeline.SourceWorkout, SourceID: "gone", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourceRun, SourceID: "gone", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourcePR, SourceID: "gone", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourceBestEffort, SourceID: "gone:5k", OccurredAt: now},
	}
	got, err := h.Hydrate(context.Background(), refs)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected all missing sources omitted, got %d", len(got))
	}
}

func TestHydrate_PRsBatchedNoNPlusOne(t *testing.T) {
	now := time.Now().UTC()
	wRepo := &fakeWorkoutRepo{
		workouts: map[string]*workout.Workout{},
		prEvents: map[string]workout.PersonalRecordEvent{
			"pr1": {ID: "pr1", UserID: "u1", ExerciseID: "bench", Weight: 100, Reps: 1, Unit: user.WeightUnitPounds, AchievedAt: now},
			"pr2": {ID: "pr2", UserID: "u1", ExerciseID: "squat", Weight: 200, Reps: 1, Unit: user.WeightUnitPounds, AchievedAt: now},
			"pr3": {ID: "pr3", UserID: "u1", ExerciseID: "dead", Weight: 300, Reps: 1, Unit: user.WeightUnitPounds, AchievedAt: now},
		},
	}
	aRepo := &fakeActivityRepo{activities: map[string]*activity.Activity{}}
	h := newTimelineHydrator(wRepo, aRepo)

	refs := []timeline.PostRef{
		{UserID: "u1", SourceType: timeline.SourcePR, SourceID: "pr1", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourcePR, SourceID: "pr2", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourcePR, SourceID: "pr3", OccurredAt: now},
	}
	got, err := h.Hydrate(context.Background(), refs)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if wRepo.getPREventCalls != 1 {
		t.Errorf("GetPersonalRecordEventsByIDs called %d times, want 1 (batched)", wRepo.getPREventCalls)
	}
}
