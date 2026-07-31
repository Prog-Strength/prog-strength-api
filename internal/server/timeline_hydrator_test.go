package server

import (
	"context"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity/strength"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// --- fakes -------------------------------------------------------------

// fakeWorkoutRepo embeds strength.Repository (so unused methods panic) and
// implements only the reads the hydrator touches through the registry: the
// batched exercise hydration behind the strength descriptor's Summarize, and
// the batched PR-event read. It counts calls so the test can assert PR
// hydration is a single batch query (no N+1).
type fakeWorkoutRepo struct {
	strength.Repository
	exercises            map[string][]strength.WorkoutExercise
	prEvents             map[string]strength.PersonalRecordEvent
	listExCalls          int
	getPREventCalls      int
	listPREventsByWkCall int
}

func (f *fakeWorkoutRepo) ListExercisesByWorkoutIDs(_ context.Context, ids []string) (map[string][]strength.WorkoutExercise, error) {
	f.listExCalls++
	out := make(map[string][]strength.WorkoutExercise, len(ids))
	for _, id := range ids {
		if ex, ok := f.exercises[id]; ok {
			out[id] = ex
		}
	}
	return out, nil
}

// ListPersonalRecordEventsByWorkouts backs the strength detail store's
// LoadMany, which since the stage-3 parity work bulk-loads PR events
// alongside exercises (one batched query — the summary path discards them,
// the unified list embeds them).
func (f *fakeWorkoutRepo) ListPersonalRecordEventsByWorkouts(_ context.Context, workoutIDs []string) ([]strength.PersonalRecordEvent, error) {
	f.listPREventsByWkCall++
	var out []strength.PersonalRecordEvent
	for _, wid := range workoutIDs {
		for _, e := range f.prEvents {
			if e.WorkoutID == wid {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

func (f *fakeWorkoutRepo) GetPersonalRecordEventsByIDs(_ context.Context, ids []string) ([]strength.PersonalRecordEvent, error) {
	f.getPREventCalls++
	var out []strength.PersonalRecordEvent
	for _, id := range ids {
		if e, ok := f.prEvents[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// fakeActivityRepo embeds activity.Repository and implements the two reads the
// session/best-effort hydration needs: SummariesByIDs (the batched base read
// for session cards, user-scoped) and Get (the single-id load carrying the
// best-effort list SummariesByIDs omits).
type fakeActivityRepo struct {
	activity.Repository
	activities map[string]*activity.Activity
	getCalls   int
	summCalls  int
}

func (f *fakeActivityRepo) SummariesByIDs(_ context.Context, userID string, ids []string) (map[string]activity.Activity, error) {
	f.summCalls++
	out := make(map[string]activity.Activity, len(ids))
	for _, id := range ids {
		a, ok := f.activities[id]
		if ok && a.UserID == userID {
			out[id] = *a
		}
	}
	return out, nil
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

// testRegistry builds a registry with the running + strength descriptors the
// session hydration renders through. Endurance summarizes off the base row so
// its detail store can be nil; strength's descriptor wraps the fake repo whose
// batched exercise read backs its Summarize.
func testRegistry(wRepo strength.Repository) *activity.Registry {
	return activity.NewRegistry(
		activity.NewEnduranceDescriptor(activity.ActivityRunning, nil),
		strength.NewDescriptor(wRepo),
	)
}

// --- tests -------------------------------------------------------------

func TestHydrate_PerSourceContent(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	wRepo := &fakeWorkoutRepo{
		exercises: map[string][]strength.WorkoutExercise{
			"w1": {
				{ExerciseID: "bench", Sets: []strength.Set{
					{Reps: 5, Weight: 100, Unit: user.WeightUnitPounds},
					{Reps: 5, Weight: 100, Unit: user.WeightUnitPounds},
				}},
				{ExerciseID: "ohp", Sets: []strength.Set{
					{Reps: 8, Weight: 50, Unit: user.WeightUnitPounds},
				}},
			},
		},
		prEvents: map[string]strength.PersonalRecordEvent{
			"pr1": {ID: "pr1", UserID: "u1", ExerciseID: "bench", Weight: 305, Reps: 3, Unit: user.WeightUnitPounds, AchievedAt: now},
		},
	}
	aRepo := &fakeActivityRepo{
		activities: map[string]*activity.Activity{
			// The lift's base row: type + name drive the card title; the
			// exercises/sets that produce its chips come from wRepo.
			"w1": {
				ID:           "w1",
				UserID:       "u1",
				ActivityType: activity.ActivityStrengthTraining,
				Name:         strptr("Push day"),
			},
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

	h := newTimelineHydrator(wRepo, aRepo, testRegistry(wRepo), nil, nil)

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
	wRepo := &fakeWorkoutRepo{exercises: map[string][]strength.WorkoutExercise{}, prEvents: map[string]strength.PersonalRecordEvent{}}
	aRepo := &fakeActivityRepo{activities: map[string]*activity.Activity{}}
	h := newTimelineHydrator(wRepo, aRepo, testRegistry(wRepo), nil, nil)

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
		exercises: map[string][]strength.WorkoutExercise{},
		prEvents: map[string]strength.PersonalRecordEvent{
			"pr1": {ID: "pr1", UserID: "u1", ExerciseID: "bench", Weight: 100, Reps: 1, Unit: user.WeightUnitPounds, AchievedAt: now},
			"pr2": {ID: "pr2", UserID: "u1", ExerciseID: "squat", Weight: 200, Reps: 1, Unit: user.WeightUnitPounds, AchievedAt: now},
			"pr3": {ID: "pr3", UserID: "u1", ExerciseID: "dead", Weight: 300, Reps: 1, Unit: user.WeightUnitPounds, AchievedAt: now},
		},
	}
	aRepo := &fakeActivityRepo{activities: map[string]*activity.Activity{}}
	h := newTimelineHydrator(wRepo, aRepo, testRegistry(wRepo), nil, nil)

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

// TestHydrate_SessionsBatchedPerAuthor pins hydrateSessions' fetch counts on a
// multi-author feed page: SummariesByIDs is user-scoped, so it runs exactly
// once per distinct author (the idsByUser grouping), while the strength detail
// load batches per TYPE across the whole merged page — one
// ListExercisesByWorkoutIDs total, even with strength posts from several
// authors. No per-post N+1 anywhere, and every post still renders.
func TestHydrate_SessionsBatchedPerAuthor(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	sets := []strength.Set{{Reps: 5, Weight: 100, Unit: user.WeightUnitPounds}}
	wRepo := &fakeWorkoutRepo{
		exercises: map[string][]strength.WorkoutExercise{
			"w1": {{ExerciseID: "bench", Sets: sets}},
			"w2": {{ExerciseID: "squat", Sets: sets}},
		},
		prEvents: map[string]strength.PersonalRecordEvent{},
	}
	aRepo := &fakeActivityRepo{
		activities: map[string]*activity.Activity{
			"w1": {ID: "w1", UserID: "u1", ActivityType: activity.ActivityStrengthTraining, Name: strptr("U1 lift")},
			"a1": {ID: "a1", UserID: "u1", ActivityType: activity.ActivityRunning, Name: strptr("U1 run"), DistanceMeters: 8046.72, DurationSeconds: 2472},
			"w2": {ID: "w2", UserID: "u2", ActivityType: activity.ActivityStrengthTraining, Name: strptr("U2 lift")},
			"a2": {ID: "a2", UserID: "u2", ActivityType: activity.ActivityRunning, Name: strptr("U2 run"), DistanceMeters: 8046.72, DurationSeconds: 2472},
		},
	}
	h := newTimelineHydrator(wRepo, aRepo, testRegistry(wRepo), nil, nil)

	refs := []timeline.PostRef{
		{UserID: "u1", SourceType: timeline.SourceWorkout, SourceID: "w1", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourceRun, SourceID: "a1", OccurredAt: now},
		{UserID: "u2", SourceType: timeline.SourceWorkout, SourceID: "w2", OccurredAt: now},
		{UserID: "u2", SourceType: timeline.SourceRun, SourceID: "a2", OccurredAt: now},
	}
	got, err := h.Hydrate(context.Background(), refs)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d contents, want 4", len(got))
	}
	for i, want := range []string{"U1 lift", "U1 run", "U2 lift", "U2 run"} {
		if got[refs[i]].Title != want {
			t.Errorf("refs[%d] title = %q, want %q", i, got[refs[i]].Title, want)
		}
	}
	if aRepo.summCalls != 2 {
		t.Errorf("SummariesByIDs called %d times, want 2 (one per author)", aRepo.summCalls)
	}
	if wRepo.listExCalls != 1 {
		t.Errorf("ListExercisesByWorkoutIDs called %d times, want 1 (one per type across the page)", wRepo.listExCalls)
	}
	// The stage-3 PR-event embed rides the same LoadMany: the hydrator pays
	// exactly one bulk PR-event query per page — never per post.
	if wRepo.listPREventsByWkCall != 1 {
		t.Errorf("ListPersonalRecordEventsByWorkouts called %d times, want 1 (one per type across the page)", wRepo.listPREventsByWkCall)
	}
}

// --- cover-photo fakes -------------------------------------------------

// fakePhotoRepo embeds activity.PhotoRepository (so unused methods panic) and
// implements only CoverPhotosByActivityIDs, counting calls so the test can pin
// the "one cover query per page" contract. covers maps an activity id to the
// cover it should return; ids absent from the map have no photo.
type fakePhotoRepo struct {
	activity.PhotoRepository
	covers    map[string]activity.PhotoCover
	coverCall int
}

func (f *fakePhotoRepo) CoverPhotosByActivityIDs(_ context.Context, activityIDs []string) (map[string]activity.PhotoCover, error) {
	f.coverCall++
	out := make(map[string]activity.PhotoCover, len(activityIDs))
	for _, id := range activityIDs {
		if c, ok := f.covers[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

// fakePhotoStore implements activity.PhotoStore with a deterministic presigned
// URL so the test can assert the thumb URL that lands on a card.
type fakePhotoStore struct {
	activity.PhotoStore
}

func (f *fakePhotoStore) PresignGet(_ context.Context, key string) (string, error) {
	return "https://cdn.test/" + key, nil
}

// TestHydrate_SessionCoverPhotosBatched pins the cover decoration: a page of N
// session posts issues EXACTLY ONE cover query (the batched DB read), and the
// presigned thumb URL + live count land on the correct session refs. Sessions
// with no cover render photoless; PR/best-effort refs never carry a cover.
func TestHydrate_SessionCoverPhotosBatched(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	sets := []strength.Set{{Reps: 5, Weight: 100, Unit: user.WeightUnitPounds}}
	wRepo := &fakeWorkoutRepo{
		exercises: map[string][]strength.WorkoutExercise{
			"w1": {{ExerciseID: "bench", Sets: sets}},
		},
		prEvents: map[string]strength.PersonalRecordEvent{
			"pr1": {ID: "pr1", UserID: "u1", ExerciseID: "bench", Weight: 305, Reps: 3, Unit: user.WeightUnitPounds, AchievedAt: now},
		},
	}
	aRepo := &fakeActivityRepo{
		activities: map[string]*activity.Activity{
			"w1": {ID: "w1", UserID: "u1", ActivityType: activity.ActivityStrengthTraining, Name: strptr("Push day")},
			"a1": {ID: "a1", UserID: "u1", ActivityType: activity.ActivityRunning, Name: strptr("Morning run"), DistanceMeters: 8046.72, DurationSeconds: 2472},
		},
	}
	pRepo := &fakePhotoRepo{
		covers: map[string]activity.PhotoCover{
			// w1 has a 2-photo cover; a1 has no photos (absent from the map).
			"w1": {Cover: activity.ActivityPhoto{ThumbS3Key: "thumbs/w1.jpg", Width: 640, Height: 480}, Count: 2},
		},
	}
	pStore := &fakePhotoStore{}
	h := newTimelineHydrator(wRepo, aRepo, testRegistry(wRepo), pRepo, pStore)

	refs := []timeline.PostRef{
		{UserID: "u1", SourceType: timeline.SourceWorkout, SourceID: "w1", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourceRun, SourceID: "a1", OccurredAt: now},
		{UserID: "u1", SourceType: timeline.SourcePR, SourceID: "pr1", OccurredAt: now},
	}
	got, err := h.Hydrate(context.Background(), refs)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	// EXACTLY one cover query for the whole page.
	if pRepo.coverCall != 1 {
		t.Errorf("CoverPhotosByActivityIDs called %d times, want 1 (one per page)", pRepo.coverCall)
	}

	// w1 carries the presigned cover + count.
	wc := got[refs[0]]
	if wc.Photo == nil {
		t.Fatalf("workout ref missing cover photo")
	}
	if wc.Photo.ThumbURL != "https://cdn.test/thumbs/w1.jpg" {
		t.Errorf("workout thumb_url = %q, want https://cdn.test/thumbs/w1.jpg", wc.Photo.ThumbURL)
	}
	if wc.Photo.Width != 640 || wc.Photo.Height != 480 {
		t.Errorf("workout photo dims = %dx%d, want 640x480", wc.Photo.Width, wc.Photo.Height)
	}
	if wc.PhotoCount != 2 {
		t.Errorf("workout photo_count = %d, want 2", wc.PhotoCount)
	}

	// a1 (run) has no cover — photoless, count 0.
	rc := got[refs[1]]
	if rc.Photo != nil {
		t.Errorf("run ref should have no cover photo, got %+v", rc.Photo)
	}
	if rc.PhotoCount != 0 {
		t.Errorf("run photo_count = %d, want 0", rc.PhotoCount)
	}

	// PR ref is not a session — never carries a cover.
	pc := got[refs[2]]
	if pc.Photo != nil || pc.PhotoCount != 0 {
		t.Errorf("pr ref should carry no cover, got photo=%+v count=%d", pc.Photo, pc.PhotoCount)
	}
}

// TestHydrate_SessionCoverPhotosNilSeam confirms graceful degradation: with a
// nil photo repo/store the session cards render photoless and no cover query is
// attempted (the seam is skipped entirely).
func TestHydrate_SessionCoverPhotosNilSeam(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	sets := []strength.Set{{Reps: 5, Weight: 100, Unit: user.WeightUnitPounds}}
	wRepo := &fakeWorkoutRepo{
		exercises: map[string][]strength.WorkoutExercise{"w1": {{ExerciseID: "bench", Sets: sets}}},
		prEvents:  map[string]strength.PersonalRecordEvent{},
	}
	aRepo := &fakeActivityRepo{
		activities: map[string]*activity.Activity{
			"w1": {ID: "w1", UserID: "u1", ActivityType: activity.ActivityStrengthTraining, Name: strptr("Push day")},
		},
	}
	h := newTimelineHydrator(wRepo, aRepo, testRegistry(wRepo), nil, nil)

	refs := []timeline.PostRef{
		{UserID: "u1", SourceType: timeline.SourceWorkout, SourceID: "w1", OccurredAt: now},
	}
	got, err := h.Hydrate(context.Background(), refs)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	wc := got[refs[0]]
	if wc.Photo != nil || wc.PhotoCount != 0 {
		t.Errorf("nil photo seam should render photoless, got photo=%+v count=%d", wc.Photo, wc.PhotoCount)
	}
}
