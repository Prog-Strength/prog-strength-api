package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// openWorkoutNoteTestDB returns a fully-migrated temp app.db for the workout
// source tests.
func openWorkoutNoteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// seedExercise inserts an exercise row so workout_exercises FK + name join
// resolve.
func seedExercise(t *testing.T, d *sql.DB, id, name string, at time.Time) {
	t.Helper()
	mustExec(t, d, `INSERT INTO exercises (id, name, description, created_at, updated_at)
		VALUES (?, ?, '', ?, ?)`, id, name, at, at)
}

// seedWorkout inserts a workout with the given workout-level note and
// updated_at.
func seedWorkout(t *testing.T, d *sql.DB, id, userID, note string, updatedAt time.Time) {
	t.Helper()
	var notesArg any
	if note == "" {
		notesArg = nil
	} else {
		notesArg = note
	}
	mustExec(t, d, `INSERT INTO workouts (id, user_id, name, performed_at, notes, created_at, updated_at)
		VALUES (?, ?, 'W', ?, ?, ?, ?)`, id, userID, updatedAt, notesArg, updatedAt, updatedAt)
}

// seedWorkoutExercise inserts a workout_exercises row (with an optional note).
func seedWorkoutExercise(t *testing.T, d *sql.DB, workoutID, exerciseID, note string, order int) {
	t.Helper()
	var notesArg any
	if note == "" {
		notesArg = nil
	} else {
		notesArg = note
	}
	mustExec(t, d, `INSERT INTO workout_exercises (workout_id, exercise_id, exercise_order, notes)
		VALUES (?, ?, ?, ?)`, workoutID, exerciseID, order, notesArg)
}

func softDeleteWorkout(t *testing.T, d *sql.DB, id string, at time.Time) {
	t.Helper()
	mustExec(t, d, `UPDATE workouts SET deleted_at = ? WHERE id = ?`, at, id)
}

func TestWorkoutNoteSource_PendingUnits(t *testing.T) {
	ctx := context.Background()
	database := openWorkoutNoteTestDB(t)

	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	settle := 10 * time.Minute
	old := now.Add(-time.Hour) // settled
	recent := now.Add(-time.Minute)

	seedExercise(t, database, "bench", "Bench Press", old)
	seedExercise(t, database, "squat", "Back Squat", old)

	// Eligible: settled, has a workout note AND an exercise note.
	seedWorkout(t, database, "w-both", "u1", "felt strong", old)
	seedWorkoutExercise(t, database, "w-both", "bench", "L shoulder cranky", 0)
	seedWorkoutExercise(t, database, "w-both", "squat", "", 1)

	// Eligible: settled, only an exercise note.
	seedWorkout(t, database, "w-exonly", "u1", "", old)
	seedWorkoutExercise(t, database, "w-exonly", "squat", "knee felt great", 0)

	// Excluded: settled but note-less.
	seedWorkout(t, database, "w-none", "u1", "", old)
	seedWorkoutExercise(t, database, "w-none", "bench", "", 0)

	// Excluded: noted but too recently updated.
	seedWorkout(t, database, "w-recent", "u1", "traveling this week", recent)

	// Excluded: noted + settled but soft-deleted.
	seedWorkout(t, database, "w-del", "u1", "deleted note", old)
	softDeleteWorkout(t, database, "w-del", old)

	src := &workoutNoteSource{db: database, settleWindow: settle}

	units, err := src.PendingUnits(ctx, now, 100)
	if err != nil {
		t.Fatalf("PendingUnits: %v", err)
	}

	got := map[string]bool{}
	for _, u := range units {
		got[u.UnitID] = true
	}
	if len(units) != 2 || !got["w-both"] || !got["w-exonly"] {
		ids := make([]string, len(units))
		for i, u := range units {
			ids[i] = u.UnitID
		}
		t.Fatalf("expected exactly [w-both w-exonly], got %v", ids)
	}

	// Verify content + provenance + prompt hint on the both-notes workout.
	var both *struct{ content, hint, srcType string }
	for i := range units {
		if units[i].UnitID == "w-both" {
			both = &struct{ content, hint, srcType string }{
				content: units[i].Content,
				hint:    units[i].PromptHint,
				srcType: units[i].Source.SourceType,
			}
			if units[i].Source.WorkoutID == nil || *units[i].Source.WorkoutID != "w-both" {
				t.Fatalf("WorkoutID provenance wrong: %+v", units[i].Source)
			}
		}
	}
	if both == nil {
		t.Fatal("w-both missing from units")
	}
	if !strings.Contains(both.content, "Workout notes: felt strong") {
		t.Fatalf("content missing workout note line: %q", both.content)
	}
	if !strings.Contains(both.content, "Bench Press: L shoulder cranky") {
		t.Fatalf("content missing exercise note line: %q", both.content)
	}
	if both.hint == "" {
		t.Fatal("PromptHint should be non-empty")
	}
	if both.srcType != "workout_note" {
		t.Fatalf("SourceType = %q, want workout_note", both.srcType)
	}
}

func TestWorkoutNoteSource_PendingUnits_Limit(t *testing.T) {
	ctx := context.Background()
	database := openWorkoutNoteTestDB(t)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	for i, id := range []string{"a", "b", "c"} {
		seedWorkout(t, database, id, "u1", "note "+id, now.Add(-time.Hour-time.Duration(i)*time.Minute))
	}
	src := &workoutNoteSource{db: database, settleWindow: 10 * time.Minute}

	units, err := src.PendingUnits(ctx, now, 2)
	if err != nil {
		t.Fatalf("PendingUnits: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("limit not honored: got %d units", len(units))
	}
}

func TestWorkoutNoteSource_CountPending(t *testing.T) {
	ctx := context.Background()
	database := openWorkoutNoteTestDB(t)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)

	for _, id := range []string{"a", "b", "c"} {
		seedWorkout(t, database, id, "u1", "note "+id, old)
	}
	// Note-less + too-recent excluded.
	seedWorkout(t, database, "none", "u1", "", old)
	seedWorkout(t, database, "recent", "u1", "note", now.Add(-time.Minute))

	src := &workoutNoteSource{db: database, settleWindow: 10 * time.Minute}

	n, err := src.CountPending(ctx, now)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if n != 3 {
		t.Fatalf("CountPending = %d, want 3 (ignores limit)", n)
	}
}

func TestWorkoutNoteSource_MarkDistilled(t *testing.T) {
	ctx := context.Background()
	database := openWorkoutNoteTestDB(t)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	seedWorkout(t, database, "w1", "u1", "note", now.Add(-time.Hour))

	src := &workoutNoteSource{db: database, settleWindow: 10 * time.Minute}
	at := now
	if err := src.MarkDistilled(ctx, "w1", at); err != nil {
		t.Fatalf("MarkDistilled: %v", err)
	}

	var marked sql.NullTime
	if err := database.QueryRowContext(ctx,
		`SELECT memory_distilled_at FROM workouts WHERE id = 'w1'`).Scan(&marked); err != nil {
		t.Fatalf("read memory_distilled_at: %v", err)
	}
	if !marked.Valid {
		t.Fatal("memory_distilled_at not set")
	}

	// Once marked it is no longer pending.
	units, err := src.PendingUnits(ctx, now, 100)
	if err != nil {
		t.Fatalf("PendingUnits: %v", err)
	}
	if len(units) != 0 {
		t.Fatalf("expected no pending units after mark, got %d", len(units))
	}
}

func TestWorkoutNoteSource_AllUndistilled_IgnoresSettleAndPaginates(t *testing.T) {
	ctx := context.Background()
	database := openWorkoutNoteTestDB(t)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	// Five noted workouts including a too-recently-updated one (settle ignored)
	// and a tie-break pair sharing updated_at.
	seedWorkout(t, database, "w1", "u1", "n1", now.Add(-5*time.Hour))
	seedWorkout(t, database, "w2", "u1", "n2", now.Add(-4*time.Hour))
	tie := now.Add(-3 * time.Hour)
	seedWorkout(t, database, "w3a", "u1", "n3a", tie)
	seedWorkout(t, database, "w3b", "u1", "n3b", tie)
	seedWorkout(t, database, "w-recent", "u1", "fresh", now.Add(-time.Second))
	// A note-less workout must still be excluded even by the backfill scan.
	seedWorkout(t, database, "w-none", "u1", "", now.Add(-6*time.Hour))

	src := &workoutNoteSource{db: database, settleWindow: 10 * time.Minute}

	var collected []string
	cursor := ""
	pages := 0
	for {
		units, next, err := src.AllUndistilled(ctx, cursor, 2)
		if err != nil {
			t.Fatalf("AllUndistilled: %v", err)
		}
		for _, u := range units {
			collected = append(collected, u.UnitID)
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}

	// Expected order is by (updated_at ASC, id ASC); the recent one is included
	// (settle ignored), the note-less one excluded.
	want := []string{"w1", "w2", "w3a", "w3b", "w-recent"}
	if len(collected) != len(want) {
		t.Fatalf("collected %v, want %v", collected, want)
	}
	for i := range want {
		if collected[i] != want[i] {
			t.Fatalf("page order wrong at %d: collected %v, want %v", i, collected, want)
		}
	}
}

func TestBuildWorkoutContent(t *testing.T) {
	tests := []struct {
		name        string
		workoutNote string
		exNotes     []exerciseNote
		want        string
	}{
		{
			name:        "workout note only",
			workoutNote: "felt strong",
			want:        "Workout notes: felt strong",
		},
		{
			name: "exercise notes only",
			exNotes: []exerciseNote{
				{Name: "Bench Press", Note: "L shoulder cranky"},
				{Name: "Back Squat", Note: ""},
				{Name: "Deadlift", Note: "PR attempt"},
			},
			want: "Bench Press: L shoulder cranky\nDeadlift: PR attempt",
		},
		{
			name:        "both",
			workoutNote: "  traveling  ",
			exNotes:     []exerciseNote{{Name: "Bench Press", Note: " hotel gym "}},
			want:        "Workout notes: traveling\nBench Press: hotel gym",
		},
		{
			name:        "name fallback to slug",
			workoutNote: "",
			exNotes:     []exerciseNote{{Name: "", Note: "felt off"}},
			want:        ": felt off",
		},
		{
			name: "all empty",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildWorkoutContent(tt.workoutNote, tt.exNotes)
			if got != tt.want {
				t.Fatalf("buildWorkoutContent = %q, want %q", got, tt.want)
			}
		})
	}
}
