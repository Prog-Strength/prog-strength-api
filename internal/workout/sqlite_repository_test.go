package workout

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/testutil/sqlcount"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Statement-count regression test. ListByUser must issue exactly three
// SQL statements regardless of how many workouts / exercises / sets the
// fixture holds:
//
//  1. SELECT workouts WHERE user_id = ? ...
//  2. SELECT workout_exercises WHERE workout_id IN (?, ?, ...)
//  3. SELECT sets WHERE workout_exercise_id IN (?, ?, ...)
//
// Pre-batched-hydration the count was 1 + N + N*M (a query per workout
// for the exercises, then a query per workout_exercise for the sets).
// If that pattern regresses, this test fails loudly. See
// prog-strength-docs/sows/workout-list-batched-hydration.md.
func TestListByUser_StatementCount(t *testing.T) {
	ctx := context.Background()
	d, counter := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seedExerciseCatalog(t, d, "barbell-bench-press", "back-squat")

	const (
		userID         = "u1"
		workoutsToSeed = 5
		setsPerEx      = 4
	)
	for i := 0; i < workoutsToSeed; i++ {
		mustCreate(t, repo, &Workout{
			UserID:      userID,
			PerformedAt: time.Date(2026, 1, i+1, 12, 0, 0, 0, time.UTC),
			Exercises: []WorkoutExercise{
				{ExerciseID: "barbell-bench-press", Order: 0, Sets: nSets(setsPerEx, 5, 185)},
				{ExerciseID: "back-squat", Order: 1, Sets: nSets(setsPerEx, 5, 225)},
			},
		})
	}

	counter.Reset()
	workouts, err := repo.ListByUser(ctx, userID, ListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if got, want := counter.N(), int64(3); got != want {
		t.Fatalf("statement count = %d, want %d (per-call budget regressed)", got, want)
	}

	// Smoke-check the result so the count assertion can't pass by
	// accident (e.g. by returning the wrong shape with fewer queries).
	if len(workouts) != workoutsToSeed {
		t.Fatalf("got %d workouts, want %d", len(workouts), workoutsToSeed)
	}
	for _, w := range workouts {
		if len(w.Exercises) != 2 {
			t.Errorf("workout %s: got %d exercises, want 2", w.ID, len(w.Exercises))
		}
		for _, we := range w.Exercises {
			if len(we.Sets) != setsPerEx {
				t.Errorf("workout %s %s: got %d sets, want %d", w.ID, we.ExerciseID, len(we.Sets), setsPerEx)
			}
		}
	}
}

// GetByID exercises the same batched helper with a single-ID input.
// Same three-statement budget: QueryRow + batched exercises + batched
// sets.
func TestGetByID_StatementCount(t *testing.T) {
	ctx := context.Background()
	d, counter := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seedExerciseCatalog(t, d, "barbell-bench-press", "back-squat")

	w := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{
			{ExerciseID: "barbell-bench-press", Order: 0, Sets: nSets(3, 5, 185)},
			{ExerciseID: "back-squat", Order: 1, Sets: nSets(3, 5, 225)},
		},
	}
	mustCreate(t, repo, w)

	counter.Reset()
	got, err := repo.GetByID(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if c := counter.N(); c != 3 {
		t.Fatalf("statement count = %d, want 3", c)
	}
	if len(got.Exercises) != 2 {
		t.Fatalf("got %d exercises, want 2", len(got.Exercises))
	}
}

// Pins the assembled shape: most-recent-first workouts, exercises in
// authored order within each workout, set counts intact. A regression
// in any of the assembly steps (sort, group, append) shows up here.
func TestListByUser_ResultShape(t *testing.T) {
	ctx := context.Background()
	d, _ := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seedExerciseCatalog(t, d, "barbell-bench-press", "back-squat", "barbell-deadlift")

	w1 := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{
			{ExerciseID: "barbell-bench-press", Order: 0, Sets: nSets(2, 5, 185)},
		},
	}
	w2 := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{
			{ExerciseID: "back-squat", Order: 0, Sets: nSets(5, 5, 225)},
			{ExerciseID: "barbell-deadlift", Order: 1, Sets: nSets(1, 3, 315)},
		},
	}
	mustCreate(t, repo, w1)
	mustCreate(t, repo, w2)

	workouts, err := repo.ListByUser(ctx, "u1", ListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(workouts) != 2 {
		t.Fatalf("got %d workouts, want 2", len(workouts))
	}
	if workouts[0].ID != w2.ID {
		t.Errorf("workouts[0] = %s, want %s", workouts[0].ID, w2.ID)
	}
	if workouts[1].ID != w1.ID {
		t.Errorf("workouts[1] = %s, want %s", workouts[1].ID, w1.ID)
	}
	wantOrder := []string{"back-squat", "barbell-deadlift"}
	gotOrder := []string{
		workouts[0].Exercises[0].ExerciseID,
		workouts[0].Exercises[1].ExerciseID,
	}
	if !reflect.DeepEqual(wantOrder, gotOrder) {
		t.Errorf("w2 exercise order = %v, want %v", gotOrder, wantOrder)
	}
	if got := len(workouts[0].Exercises[0].Sets); got != 5 {
		t.Errorf("w2 back-squat sets = %d, want 5", got)
	}
	if got := len(workouts[0].Exercises[1].Sets); got != 1 {
		t.Errorf("w2 deadlift sets = %d, want 1", got)
	}
}

// Concurrency soak: 100 concurrent ListByUser calls against a pool of
// 2 connections must all return cleanly. This pins the property that
// the batched helpers don't hold connections across nested calls — a
// regression to per-row nesting would deadlock on the second
// concurrent call. MaxOpenConns=2 is the smallest pool that still
// serves a single batched ListByUser, while guaranteeing deadlock
// under the legacy nested pattern.
func TestListByUser_NoPoolDeadlock(t *testing.T) {
	ctx := context.Background()
	d, _ := newTestDB(t)
	d.SetMaxOpenConns(2)
	d.SetMaxIdleConns(2)
	repo := NewSQLiteRepository(d)
	seedExerciseCatalog(t, d, "barbell-bench-press", "back-squat")

	mustCreate(t, repo, &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Exercises: []WorkoutExercise{
			{ExerciseID: "barbell-bench-press", Order: 0, Sets: nSets(3, 5, 185)},
			{ExerciseID: "back-squat", Order: 1, Sets: nSets(3, 5, 225)},
		},
	})

	const N = 100
	errCh := make(chan error, N)
	timeout := time.After(15 * time.Second)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := repo.ListByUser(ctx, "u1", ListOptions{Limit: 50})
			errCh <- err
		}()
	}
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-timeout:
		t.Fatalf("ListByUser deadlocked: %d/%d goroutines still in flight after 15s", N-len(errCh), N)
	}
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("ListByUser: %v", err)
		}
	}
}

// --- helpers ------------------------------------------------------

// newTestDB opens a tempfile-backed SQLite via the counting driver,
// applies the project migrations, and returns the DB + counter.
// Tempfile (not :memory:) so MaxOpenConns > 1 works — `:memory:` gives
// each connection its own private database.
func newTestDB(t *testing.T) (*sql.DB, *sqlcount.Counter) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	dsn := path + "?_foreign_keys=on&_journal_mode=WAL"
	sqlDB, counter, err := sqlcount.Open(dsn)
	if err != nil {
		t.Fatalf("sqlcount.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return sqlDB, counter
}

// seedExerciseCatalog inserts minimal exercise rows so the FK on
// workout_exercises.exercise_id is satisfied. The rows have empty
// muscle_groups/equipment because the workout tests don't exercise
// those columns.
func seedExerciseCatalog(t *testing.T, d *sql.DB, slugs ...string) {
	t.Helper()
	now := time.Now().UTC()
	for _, slug := range slugs {
		if _, err := d.ExecContext(context.Background(), `
			INSERT INTO exercises (id, name, description, created_at, updated_at, deleted_at)
			VALUES (?, ?, '', ?, ?, NULL)
		`, slug, slug, now, now); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}
}

// mustCreate is a Create() wrapper that fatals on error so test bodies
// stay readable.
func mustCreate(t *testing.T, repo *SQLiteRepository, w *Workout) {
	t.Helper()
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// nSets builds a slice of n identical Sets at the given reps/weight.
// Pounds is the unit; tests don't care about the choice.
func nSets(n, reps int, weight float64) []Set {
	out := make([]Set, n)
	for i := range out {
		out[i] = Set{Reps: reps, Weight: weight, Unit: user.WeightUnitPounds}
	}
	return out
}

// TestListCompletedSessionsSince_Filters verifies the projection excludes
// end-less workouts, soft-deleted workouts, and workouts performed before the
// since cutoff, returning only live completed sessions in range.
func TestListCompletedSessionsSince_Filters(t *testing.T) {
	ctx := context.Background()
	d, _ := newTestDB(t)
	repo := NewSQLiteRepository(d)
	seedExerciseCatalog(t, d, "back-squat")

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := func(start time.Time, mins int) *time.Time {
		e := start.Add(time.Duration(mins) * time.Minute)
		return &e
	}

	// In-range, completed: included.
	inRangeStart := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	included := &Workout{
		UserID:      "u1",
		PerformedAt: inRangeStart,
		EndedAt:     end(inRangeStart, 75),
		Exercises:   []WorkoutExercise{{ExerciseID: "back-squat", Order: 0, Sets: nSets(3, 5, 225)}},
	}
	mustCreate(t, repo, included)

	// End-less: excluded.
	endless := &Workout{
		UserID:      "u1",
		PerformedAt: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
		Exercises:   []WorkoutExercise{{ExerciseID: "back-squat", Order: 0, Sets: nSets(3, 5, 225)}},
	}
	mustCreate(t, repo, endless)

	// Before since: excluded.
	beforeStart := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	before := &Workout{
		UserID:      "u1",
		PerformedAt: beforeStart,
		EndedAt:     end(beforeStart, 60),
		Exercises:   []WorkoutExercise{{ExerciseID: "back-squat", Order: 0, Sets: nSets(3, 5, 225)}},
	}
	mustCreate(t, repo, before)

	// Soft-deleted (in range, completed): excluded.
	delStart := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	deleted := &Workout{
		UserID:      "u1",
		PerformedAt: delStart,
		EndedAt:     end(delStart, 90),
		Exercises:   []WorkoutExercise{{ExerciseID: "back-squat", Order: 0, Sets: nSets(3, 5, 225)}},
	}
	mustCreate(t, repo, deleted)
	if err := repo.Delete(ctx, deleted.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Other user (in range, completed): excluded by user scoping.
	otherStart := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	other := &Workout{
		UserID:      "u2",
		PerformedAt: otherStart,
		EndedAt:     end(otherStart, 45),
		Exercises:   []WorkoutExercise{{ExerciseID: "back-squat", Order: 0, Sets: nSets(3, 5, 225)}},
	}
	mustCreate(t, repo, other)

	got, err := repo.ListCompletedSessionsSince(ctx, "u1", since)
	if err != nil {
		t.Fatalf("ListCompletedSessionsSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(got), got)
	}
	if !got[0].PerformedAt.Equal(inRangeStart) {
		t.Fatalf("PerformedAt = %v, want %v", got[0].PerformedAt, inRangeStart)
	}
	if got := got[0].EndedAt.Sub(got[0].PerformedAt).Minutes(); got != 75 {
		t.Fatalf("duration = %v min, want 75", got)
	}
}
