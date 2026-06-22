package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// workoutNotePromptHint frames a workout's free-text notes for the distiller so
// terse training-log shorthand is mined for durable facts rather than one-off
// session minutiae. Appended to the shared distiller system prompt.
const workoutNotePromptHint = `The following are terse free-text notes a user wrote in their training log for a single workout. They are abbreviated and shorthand (e.g. "L shoulder cranky, dropped to 3x5", "hotel gym only this week"). Extract only durable, stable facts worth remembering across future sessions — recurring injuries or limitations, equipment/travel constraints, and lasting preferences. Ignore one-off in-session minutiae and anything that merely restates the logged sets/weights.`

// workoutNoteSource distills one workout's free-text notes (workouts.notes plus
// its workout_exercises.notes) into durable memories. It reads app.db directly
// because a unit spans workouts, workout_exercises, and exercises — consistent
// with the chat adapter wrapping the chat repo. settleWindow is the workout
// settle window (cfg.WorkoutSettleMinutes); the source owns its own cutoff so
// the job never computes one.
type workoutNoteSource struct {
	db           *sql.DB
	settleWindow time.Duration
}

var _ vectormemory.MemorySource = (*workoutNoteSource)(nil)

func (s *workoutNoteSource) SourceType() string { return "workout_note" }

// PendingUnits returns settled (updated_at older than now-settleWindow),
// undistilled, non-deleted workouts that carry at least one non-empty note
// (workout-level OR any exercise-level), oldest-settled first, up to limit.
func (s *workoutNoteSource) PendingUnits(ctx context.Context, now time.Time, limit int) ([]vectormemory.DistillUnit, error) {
	cutoff := now.Add(-s.settleWindow).UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.user_id
		FROM workouts w
		WHERE w.deleted_at IS NULL
		  AND w.memory_distilled_at IS NULL
		  AND w.updated_at < ?
		  AND (
		    (w.notes IS NOT NULL AND TRIM(w.notes) <> '')
		    OR EXISTS (
		      SELECT 1 FROM workout_exercises we
		      WHERE we.workout_id = w.id
		        AND we.notes IS NOT NULL AND TRIM(we.notes) <> ''
		    )
		  )
		ORDER BY w.updated_at ASC
		LIMIT ?
	`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	refs, _, err := scanWorkoutIDRows(rows, false)
	if err != nil {
		return nil, err
	}
	return s.assembleUnits(ctx, refs)
}

// CountPending mirrors PendingUnits' WHERE without the LIMIT, feeding the idle
// backlog gauge (which the capped PendingUnits cannot).
func (s *workoutNoteSource) CountPending(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-s.settleWindow).UTC()
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM workouts w
		WHERE w.deleted_at IS NULL
		  AND w.memory_distilled_at IS NULL
		  AND w.updated_at < ?
		  AND (
		    (w.notes IS NOT NULL AND TRIM(w.notes) <> '')
		    OR EXISTS (
		      SELECT 1 FROM workout_exercises we
		      WHERE we.workout_id = w.id
		        AND we.notes IS NOT NULL AND TRIM(we.notes) <> ''
		    )
		  )
	`, cutoff).Scan(&n)
	return n, err
}

// AllUndistilled is PendingUnits without the settle clause, keyset-paginated on
// (updated_at, id) via an opaque base64 cursor, for the one-time backfill. An
// empty cursor starts at the beginning; an empty returned cursor means the set
// is exhausted.
func (s *workoutNoteSource) AllUndistilled(ctx context.Context, cursor string, limit int) ([]vectormemory.DistillUnit, string, error) {
	afterUpdatedAt, afterID, err := decodeWorkoutCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	// Keyset pagination on (updated_at, id): the empty-cursor case uses a zero
	// time that sorts before every real row. The tuple predicate is spelled out
	// (rather than SQLite row-value comparison) so it reads identically on every
	// build.
	//
	// why time.Time (not a string) for updated_at: go-sqlite3 reformats a stored
	// timestamp to RFC3339 on scan, so a scanned-back string would not
	// text-compare against the stored form. Binding a time.Time on both sides has
	// the driver format the column and the cursor value identically.
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.user_id, w.updated_at
		FROM workouts w
		WHERE w.deleted_at IS NULL
		  AND w.memory_distilled_at IS NULL
		  AND (w.updated_at > ? OR (w.updated_at = ? AND w.id > ?))
		  AND (
		    (w.notes IS NOT NULL AND TRIM(w.notes) <> '')
		    OR EXISTS (
		      SELECT 1 FROM workout_exercises we
		      WHERE we.workout_id = w.id
		        AND we.notes IS NOT NULL AND TRIM(we.notes) <> ''
		    )
		  )
		ORDER BY w.updated_at ASC, w.id ASC
		LIMIT ?
	`, afterUpdatedAt.UTC(), afterUpdatedAt.UTC(), afterID, limit)
	if err != nil {
		return nil, "", err
	}
	refs, lastUpdatedAt, err := scanWorkoutIDRows(rows, true)
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(refs) == limit {
		next = encodeWorkoutCursor(lastUpdatedAt, refs[len(refs)-1].ID)
	}
	units, err := s.assembleUnits(ctx, refs)
	if err != nil {
		return nil, "", err
	}
	return units, next, nil
}

func (s *workoutNoteSource) MarkDistilled(ctx context.Context, unitID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workouts SET memory_distilled_at = ? WHERE id = ?`, at.UTC(), unitID)
	return err
}

// workoutRef pairs a workout's id with the user_id scanned alongside it, so
// assembleUnits can reuse the already-fetched user_id rather than re-querying it.
type workoutRef struct {
	ID     string
	UserID string
}

// scanWorkoutIDRows drains a query selecting (id, user_id[, updated_at]) into
// the workout ref list and the last scanned updated_at (for cursor encoding).
// withUpdatedAt toggles whether the rows carry the trailing updated_at column.
func scanWorkoutIDRows(rows *sql.Rows, withUpdatedAt bool) ([]workoutRef, time.Time, error) {
	defer rows.Close()
	var (
		refs          []workoutRef
		lastUpdatedAt time.Time
	)
	for rows.Next() {
		var ref workoutRef
		if withUpdatedAt {
			if err := rows.Scan(&ref.ID, &ref.UserID, &lastUpdatedAt); err != nil {
				return nil, time.Time{}, err
			}
		} else if err := rows.Scan(&ref.ID, &ref.UserID); err != nil {
			return nil, time.Time{}, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return refs, lastUpdatedAt, nil
}

// exerciseNote is one ordered exercise label + its note, used by
// buildWorkoutContent.
type exerciseNote struct {
	Name string
	Note string
}

// assembleUnits loads each workout's note plus its ordered exercise notes and
// composes a self-contained DistillUnit per workout. One query per workout is
// fine at this volume.
func (s *workoutNoteSource) assembleUnits(ctx context.Context, refs []workoutRef) ([]vectormemory.DistillUnit, error) {
	units := make([]vectormemory.DistillUnit, 0, len(refs))
	for _, ref := range refs {
		var workoutNote sql.NullString
		if err := s.db.QueryRowContext(ctx,
			`SELECT notes FROM workouts WHERE id = ?`, ref.ID).Scan(&workoutNote); err != nil {
			return nil, err
		}

		exNotes, err := s.loadExerciseNotes(ctx, ref.ID)
		if err != nil {
			return nil, err
		}

		content := buildWorkoutContent(workoutNote.String, exNotes)
		wid := ref.ID
		units = append(units, vectormemory.DistillUnit{
			UnitID:     ref.ID,
			UserID:     ref.UserID,
			Content:    content,
			PromptHint: workoutNotePromptHint,
			Source:     vectormemory.Provenance{SourceType: "workout_note", WorkoutID: &wid},
		})
	}
	return units, nil
}

// loadExerciseNotes returns the workout's exercise notes in exercise_order,
// each labeled by its exercise display name (falling back to the exercise_id
// slug when the join finds no name).
func (s *workoutNoteSource) loadExerciseNotes(ctx context.Context, workoutID string) ([]exerciseNote, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(e.name, ''), we.exercise_id, COALESCE(we.notes, '')
		FROM workout_exercises we
		LEFT JOIN exercises e ON e.id = we.exercise_id
		WHERE we.workout_id = ?
		ORDER BY we.exercise_order ASC
	`, workoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []exerciseNote
	for rows.Next() {
		var name, exerciseID, note string
		if err := rows.Scan(&name, &exerciseID, &note); err != nil {
			return nil, err
		}
		label := name
		if strings.TrimSpace(label) == "" {
			label = exerciseID
		}
		out = append(out, exerciseNote{Name: label, Note: note})
	}
	return out, rows.Err()
}

// buildWorkoutContent composes a workout's notes into one lightly-structured
// blob: a leading "Workout notes: <note>" line when the workout note is
// non-empty, then a "<exercise name>: <note>" line per exercise note (in the
// caller-supplied order) whose note is non-empty. Joined with newlines and
// trimmed. Kept as a free function so it is unit-testable in isolation.
func buildWorkoutContent(workoutNote string, exNotes []exerciseNote) string {
	var lines []string
	if trimmed := strings.TrimSpace(workoutNote); trimmed != "" {
		lines = append(lines, "Workout notes: "+trimmed)
	}
	for _, en := range exNotes {
		note := strings.TrimSpace(en.Note)
		if note == "" {
			continue
		}
		name := strings.TrimSpace(en.Name)
		lines = append(lines, name+": "+note)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// encodeWorkoutCursor base64-encodes the keyset position
// "<updated_at RFC3339Nano>|<id>" so the cursor is opaque to the caller.
func encodeWorkoutCursor(updatedAt time.Time, id string) string {
	return base64.StdEncoding.EncodeToString([]byte(updatedAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

// decodeWorkoutCursor reverses encodeWorkoutCursor. An empty cursor decodes to a
// zero time and empty id, which sort before every real row, so the first page
// starts at the beginning.
func decodeWorkoutCursor(cursor string) (updatedAt time.Time, id string, err error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("server: decode workout cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("server: malformed workout cursor %q", cursor)
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("server: malformed workout cursor timestamp %q: %w", parts[0], err)
	}
	return at, parts[1], nil
}
