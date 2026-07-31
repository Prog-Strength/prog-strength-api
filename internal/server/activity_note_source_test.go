package server

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
)

// seedNoteUser inserts the user row the source joins for the timezone.
func seedNoteUser(t *testing.T, d *sql.DB, id, timezone string) {
	t.Helper()
	mustExec(t, d, `INSERT INTO users (id, email, display_name, weight_unit, timezone, created_at, updated_at)
		VALUES (?, ?, 'Runner', 'lb', ?, '2026-07-01', '2026-07-01')`, id, id+"@example.com", timezone)
}

// seedEnduranceActivity inserts an endurance session (base row + its detail
// row, so the distance projection resolves) with the given note and
// updated_at. An empty note is stored NULL, matching the write path.
func seedEnduranceActivity(t *testing.T, d *sql.DB, id, userID, actType, name string, startTime time.Time, distanceMeters float64, durationSeconds int, note string, updatedAt time.Time) {
	t.Helper()
	var notesArg any
	if note != "" {
		notesArg = note
	}
	mustExec(t, d, `INSERT INTO activities (id, user_id, activity_type, ingest_source, name, start_time, duration_seconds, notes, created_at, updated_at)
		VALUES (?, ?, ?, 'manual_tcx', ?, ?, ?, ?, ?, ?)`,
		id, userID, actType, name, startTime, durationSeconds, notesArg, updatedAt, updatedAt)
	table := map[string]string{
		"running": "activity_run_details",
		"hiking":  "activity_hike_details",
		"walking": "activity_walk_details",
		"cycling": "activity_cycle_details",
		"other":   "activity_other_details",
	}[actType]
	if table == "" {
		t.Fatalf("no detail table for %q", actType)
	}
	mustExec(t, d, `INSERT INTO `+table+` (activity_id, distance_meters, raw_distance_meters, environment)
		VALUES (?, ?, ?, 'outdoor')`, id, distanceMeters, distanceMeters)
}

// newActivityNoteSource builds the source over a migrated temp db plus a real
// activity repository (the source reads session context through the canonical
// projection, not its own SQL).
func newActivityNoteSource(t *testing.T, settle time.Duration) (*activityNoteSource, *sql.DB) {
	t.Helper()
	database := openWorkoutNoteTestDB(t)
	repo := activity.NewSQLiteRepository(database, activity.NewMemoryArchiver())
	return &activityNoteSource{db: database, activities: repo, settleWindow: settle}, database
}

func TestActivityNoteSource_PendingUnits(t *testing.T) {
	ctx := context.Background()
	src, database := newActivityNoteSource(t, 10*time.Minute)

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)      // settled
	recent := now.Add(-time.Minute) // still settling
	start := time.Date(2026, 7, 21, 13, 30, 0, 0, time.UTC)

	seedNoteUser(t, database, "u1", "America/New_York")

	// Eligible: settled run with a note.
	seedEnduranceActivity(t, database, "a-run", "u1", "running", "Morning Run", start, 8046.72, 2472, "legs were dead the first two miles", old)
	// Eligible: settled hike with a note (the source is not running-only).
	seedEnduranceActivity(t, database, "a-hike", "u1", "hiking", "", start, 12000, 14400, "knee twinge on the descent", old)
	// Excluded: settled but note-less.
	seedEnduranceActivity(t, database, "a-quiet", "u1", "running", "", start, 5000, 1500, "", old)
	// Excluded: noted but still settling.
	seedEnduranceActivity(t, database, "a-recent", "u1", "running", "", start, 5000, 1500, "still typing", recent)
	// Excluded: noted + settled but soft-deleted.
	seedEnduranceActivity(t, database, "a-del", "u1", "running", "", start, 5000, 1500, "deleted note", old)
	mustExec(t, database, `UPDATE activities SET deleted_at = ? WHERE id = 'a-del'`, old)
	// Excluded: already distilled.
	seedEnduranceActivity(t, database, "a-done", "u1", "running", "", start, 5000, 1500, "already mined", old)
	mustExec(t, database, `UPDATE activities SET memory_distilled_at = ? WHERE id = 'a-done'`, old)
	// Excluded: strength training belongs to the workout-note source, which
	// fuses in the per-exercise notes this one knows nothing about.
	mustExec(t, database, `INSERT INTO activities (id, user_id, activity_type, ingest_source, name, start_time, notes, created_at, updated_at)
		VALUES ('a-lift', 'u1', 'strength_training', 'manual', 'Push', ?, 'felt strong', ?, ?)`, start, old, old)

	units, err := src.PendingUnits(ctx, now, 100)
	if err != nil {
		t.Fatalf("PendingUnits: %v", err)
	}
	got := map[string]string{}
	for _, u := range units {
		got[u.UnitID] = u.Content
	}
	if len(units) != 2 || got["a-run"] == "" || got["a-hike"] == "" {
		ids := make([]string, 0, len(units))
		for _, u := range units {
			ids = append(ids, u.UnitID)
		}
		t.Fatalf("PendingUnits = %v, want exactly [a-run a-hike]", ids)
	}

	// The run's content carries the note under a context header naming the
	// sport, the user's LOCAL date (start is 13:30 UTC = 09:30 in New York),
	// the session name, and its distance/duration.
	content := got["a-run"]
	for _, want := range []string{"running", "2026-07-21", "Morning Run", "5.0 mi", "41:12", "legs were dead the first two miles"} {
		if !strings.Contains(content, want) {
			t.Errorf("run content missing %q:\n%s", want, content)
		}
	}

	// Provenance points at the activities row through the shared FK.
	for _, u := range units {
		if u.Source.SourceType != "activity_note" {
			t.Errorf("source type = %q, want activity_note", u.Source.SourceType)
		}
		if u.Source.WorkoutID == nil || *u.Source.WorkoutID != u.UnitID {
			t.Errorf("provenance activity id = %v, want %s", u.Source.WorkoutID, u.UnitID)
		}
		if u.Source.SessionID != nil {
			t.Errorf("chat session id must stay nil, got %v", u.Source.SessionID)
		}
		if u.PromptHint == "" {
			t.Error("prompt hint should frame the note for the distiller")
		}
		if u.UserID != "u1" {
			t.Errorf("user id = %q, want u1", u.UserID)
		}
	}
}

func TestActivityNoteSource_CountPendingAndMarkDistilled(t *testing.T) {
	ctx := context.Background()
	src, database := newActivityNoteSource(t, 10*time.Minute)

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	start := time.Date(2026, 7, 21, 13, 30, 0, 0, time.UTC)
	seedNoteUser(t, database, "u1", "UTC")
	for _, id := range []string{"a1", "a2", "a3"} {
		seedEnduranceActivity(t, database, id, "u1", "running", "", start, 5000, 1500, "a note", old)
	}

	// CountPending is uncapped where PendingUnits is capped — that gap is the
	// whole reason the backlog gauge needs its own method.
	if n, err := src.CountPending(ctx, now); err != nil || n != 3 {
		t.Fatalf("CountPending = %d, %v; want 3, nil", n, err)
	}
	units, err := src.PendingUnits(ctx, now, 2)
	if err != nil || len(units) != 2 {
		t.Fatalf("PendingUnits(limit 2) = %d units, %v; want 2, nil", len(units), err)
	}

	at := time.Date(2026, 7, 22, 12, 5, 0, 0, time.UTC)
	if err := src.MarkDistilled(ctx, "a1", at); err != nil {
		t.Fatalf("MarkDistilled: %v", err)
	}
	var stamped sql.NullTime
	if err := database.QueryRow(`SELECT memory_distilled_at FROM activities WHERE id = 'a1'`).Scan(&stamped); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !stamped.Valid || !stamped.Time.Equal(at) {
		t.Errorf("memory_distilled_at = %v, want %v", stamped, at)
	}
	if n, err := src.CountPending(ctx, now); err != nil || n != 2 {
		t.Fatalf("CountPending after marking = %d, %v; want 2, nil", n, err)
	}
}

func TestActivityNoteSource_AllUndistilledPaginates(t *testing.T) {
	ctx := context.Background()
	src, database := newActivityNoteSource(t, 10*time.Minute)

	start := time.Date(2026, 7, 21, 13, 30, 0, 0, time.UTC)
	seedNoteUser(t, database, "u1", "UTC")
	// Deliberately all updated "just now": AllUndistilled ignores the settle
	// window, so a note written seconds ago is still backfillable.
	base := time.Date(2026, 7, 22, 11, 59, 0, 0, time.UTC)
	for i, id := range []string{"b1", "b2", "b3"} {
		seedEnduranceActivity(t, database, id, "u1", "running", "", start, 5000, 1500, "note "+id, base.Add(time.Duration(i)*time.Second))
	}

	var seen []string
	cursor := ""
	for range 5 {
		units, next, err := src.AllUndistilled(ctx, cursor, 2)
		if err != nil {
			t.Fatalf("AllUndistilled(%q): %v", cursor, err)
		}
		for _, u := range units {
			seen = append(seen, u.UnitID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 3 || seen[0] != "b1" || seen[2] != "b3" {
		t.Fatalf("AllUndistilled drained %v, want [b1 b2 b3] in updated_at order", seen)
	}
}
