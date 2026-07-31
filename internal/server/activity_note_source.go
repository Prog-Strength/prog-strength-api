package server

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// activityNotePromptHint frames an endurance session's note for the distiller.
// It deliberately steers away from the numbers — distance, pace, and splits are
// structured data the coach already reads directly; the note's value is the
// part the watch cannot record.
const activityNotePromptHint = `The following is a terse free-text note a user wrote about one endurance session (a run, hike, walk, or ride), preceded by a one-line header naming the sport, date, and headline metrics. The note is shorthand (e.g. "legs were dead the first two miles then it clicked", "humid, backed off on purpose", "knee twinge at mile 4"). Extract only durable, stable facts worth remembering across future sessions — recurring niggles and injuries, conditions or terrain that reliably affect this athlete, equipment, and lasting preferences or patterns. Ignore one-off session minutiae, and never restate the distance, pace, or duration from the header: those are already recorded as structured data.`

// activityNoteSource distills the free-text note on a NON-strength activity
// (run, hike, walk, ride, other) into durable memories. Lifts are excluded
// because workoutNoteSource already owns them and fuses in the per-exercise
// notes this source knows nothing about — one activity is never eligible for
// both.
//
// Eligibility is answered from base columns alone (db), but the context header
// needs the session's distance, which since unification lives in a per-type
// detail table. Rather than re-derive that join, the source reads it back
// through the activity repository's canonical projection (activities), so a
// sixth detail table needs no change here. settleWindow is
// cfg.ActivitySettleMinutes; the source owns its own cutoff so the job never
// computes one.
type activityNoteSource struct {
	db           *sql.DB
	activities   activity.Repository
	settleWindow time.Duration
}

var _ vectormemory.MemorySource = (*activityNoteSource)(nil)

func (s *activityNoteSource) SourceType() string { return "activity_note" }

// eligibleColumns is the selection shared by the three queries below: the ids
// plus the base-row context the header needs that the repository projection
// doesn't carry (the author's timezone, for rendering a LOCAL session date).
const eligibleColumns = `a.id, a.user_id, COALESCE(u.timezone, 'UTC')`

// notedActivityPredicate is the eligibility rule: a live, not-yet-distilled,
// non-strength session carrying a non-empty note. The settle clause is the
// caller's to add — PendingUnits waits for the note to stop changing, the
// backfill does not.
const notedActivityPredicate = `
	a.activity_type <> 'strength_training'
	  AND a.deleted_at IS NULL
	  AND a.memory_distilled_at IS NULL
	  AND a.notes IS NOT NULL AND TRIM(a.notes) <> ''`

// PendingUnits returns settled (updated_at older than now-settleWindow),
// undistilled, non-deleted endurance sessions carrying a note, oldest-settled
// first, up to limit.
func (s *activityNoteSource) PendingUnits(ctx context.Context, now time.Time, limit int) ([]vectormemory.DistillUnit, error) {
	cutoff := now.Add(-s.settleWindow).UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+eligibleColumns+`
		FROM activities a
		JOIN users u ON u.id = a.user_id
		WHERE `+notedActivityPredicate+`
		  AND a.updated_at < ?
		ORDER BY a.updated_at ASC
		LIMIT ?
	`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	refs, _, err := scanActivityNoteRows(rows, false)
	if err != nil {
		return nil, err
	}
	return s.assembleUnits(ctx, refs)
}

// CountPending mirrors PendingUnits' WHERE without the LIMIT, feeding the idle
// backlog gauge (which the capped PendingUnits cannot).
func (s *activityNoteSource) CountPending(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-s.settleWindow).UTC()
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM activities a
		WHERE `+notedActivityPredicate+`
		  AND a.updated_at < ?
	`, cutoff).Scan(&n)
	return n, err
}

// AllUndistilled is PendingUnits without the settle clause, keyset-paginated on
// (updated_at, id) via an opaque base64 cursor, for the one-time backfill. It
// shares the workout source's cursor codec — same keyset, same encoding.
func (s *activityNoteSource) AllUndistilled(ctx context.Context, cursor string, limit int) ([]vectormemory.DistillUnit, string, error) {
	afterUpdatedAt, afterID, err := decodeWorkoutCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+eligibleColumns+`, a.updated_at
		FROM activities a
		JOIN users u ON u.id = a.user_id
		WHERE `+notedActivityPredicate+`
		  AND (a.updated_at > ? OR (a.updated_at = ? AND a.id > ?))
		ORDER BY a.updated_at ASC, a.id ASC
		LIMIT ?
	`, afterUpdatedAt.UTC(), afterUpdatedAt.UTC(), afterID, limit)
	if err != nil {
		return nil, "", err
	}
	refs, lastUpdatedAt, err := scanActivityNoteRows(rows, true)
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

func (s *activityNoteSource) MarkDistilled(ctx context.Context, unitID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE activities SET memory_distilled_at = ? WHERE id = ?`, at.UTC(), unitID)
	return err
}

// activityNoteRef is an eligible session's id plus the context scanned
// alongside it, so assembleUnits doesn't re-query what the sweep already read.
type activityNoteRef struct {
	ID       string
	UserID   string
	Timezone string
}

// scanActivityNoteRows drains a query selecting (id, user_id, timezone[,
// updated_at]) into the ref list and the last scanned updated_at (for cursor
// encoding). withUpdatedAt toggles the trailing column.
func scanActivityNoteRows(rows *sql.Rows, withUpdatedAt bool) ([]activityNoteRef, time.Time, error) {
	defer rows.Close()
	var (
		refs          []activityNoteRef
		lastUpdatedAt time.Time
	)
	for rows.Next() {
		var ref activityNoteRef
		if withUpdatedAt {
			if err := rows.Scan(&ref.ID, &ref.UserID, &ref.Timezone, &lastUpdatedAt); err != nil {
				return nil, time.Time{}, err
			}
		} else if err := rows.Scan(&ref.ID, &ref.UserID, &ref.Timezone); err != nil {
			return nil, time.Time{}, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return refs, lastUpdatedAt, nil
}

// assembleUnits turns each eligible session into a self-contained DistillUnit:
// a one-line context header plus the note. The header exists so an observation
// like "felt flat the whole way" is distilled knowing it came off an 8-mile
// hike rather than a recovery jog. Sessions are read back per author through
// the repository's batch summary read.
func (s *activityNoteSource) assembleUnits(ctx context.Context, refs []activityNoteRef) ([]vectormemory.DistillUnit, error) {
	idsByUser := make(map[string][]string)
	for _, ref := range refs {
		idsByUser[ref.UserID] = append(idsByUser[ref.UserID], ref.ID)
	}
	sessions := make(map[string]activity.Activity)
	for userID, ids := range idsByUser {
		found, err := s.activities.SummariesByIDs(ctx, userID, ids)
		if err != nil {
			return nil, err
		}
		for id, a := range found {
			sessions[id] = a
		}
	}

	units := make([]vectormemory.DistillUnit, 0, len(refs))
	for _, ref := range refs {
		a, ok := sessions[ref.ID]
		if !ok {
			// Deleted between the sweep and the read. Skip rather than distill
			// a session we can no longer describe; the row is left unstamped,
			// and it is gone from the next sweep anyway.
			continue
		}
		id := ref.ID
		units = append(units, vectormemory.DistillUnit{
			UnitID:     id,
			UserID:     ref.UserID,
			Content:    buildActivityNoteContent(a, ref.Timezone),
			PromptHint: activityNotePromptHint,
			Source:     vectormemory.Provenance{SourceType: "activity_note", WorkoutID: &id},
		})
	}
	return units, nil
}

// buildActivityNoteContent composes the header + note blob, e.g.
//
//	running · 2026-07-21 · Morning Run · 5.0 mi · 41:12
//	Notes: legs were dead the first two miles
//
// The date is the author's LOCAL calendar date: a 9pm run stored in UTC would
// otherwise be attributed to the following day, which matters when a memory
// says "always struggles on Mondays". An unparseable timezone degrades to UTC
// rather than dropping the header.
func buildActivityNoteContent(a activity.Activity, timezone string) string {
	parts := []string{string(a.ActivityType), localDate(a.StartTime, timezone)}
	if a.Name != nil && strings.TrimSpace(*a.Name) != "" {
		parts = append(parts, strings.TrimSpace(*a.Name))
	}
	if a.DistanceMeters > 0 {
		parts = append(parts, activity.FormatMiles(a.DistanceMeters))
	}
	if a.DurationSeconds > 0 {
		parts = append(parts, activity.FormatDuration(float64(a.DurationSeconds)))
	}
	note := ""
	if a.Notes != nil {
		note = strings.TrimSpace(*a.Notes)
	}
	return strings.Join(parts, " · ") + "\nNotes: " + note
}

// localDate renders t as a YYYY-MM-DD calendar date in the named IANA zone.
func localDate(t time.Time, timezone string) string {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format(time.DateOnly)
}
