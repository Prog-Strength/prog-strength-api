package db

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// migration028 backfills planned_workout completion links by replaying a frozen
// copy of the live matcher (same-day + kind + nearest scheduled start, timezone
// bucketed via time.LoadLocation) over already-logged running activities and
// workouts, oldest-first. DB-only and frozen: it never calls service code, so a
// rebuilt DB always reconciles identically regardless of future matcher changes.
func migration028() goMigration {
	return goMigration{
		Version: 28,
		Name:    "backfill_planned_workout_links",
		Run:     backfillPlannedWorkoutLinks,
	}
}

type bfSession struct {
	id        string
	userID    string
	startUTC  time.Time
	kind      string // "activity" (running) or "workout" (lift)
	createdAt time.Time
}

type bfPlan struct {
	id           string
	activityKind string
	startUTC     time.Time
	timezone     string
}

func backfillPlannedWorkoutLinks(ctx context.Context, tx *sql.Tx) error {
	sessions, err := bfLoadSessions(ctx, tx)
	if err != nil {
		return err
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].createdAt.Before(sessions[j].createdAt) })

	// updated_at reflects when this backfill links the plan, mirroring the live
	// SetCompletion write (which stamps the current time, not the session start).
	now := time.Now().UTC()
	for _, s := range sessions {
		wantKind := "lift"
		if s.kind == "activity" {
			wantKind = "run"
		}
		plan, err := bfSelectPlan(ctx, tx, s, wantKind)
		if err != nil {
			return err
		}
		if plan == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE planned_workouts
			SET status = 'completed', completed_session_id = ?, completed_session_kind = ?, updated_at = ?
			WHERE id = ? AND status = 'planned' AND deleted_at IS NULL
		`, s.id, s.kind, now, plan.id); err != nil {
			return err
		}
	}
	return nil
}

func bfLoadSessions(ctx context.Context, tx *sql.Tx) ([]bfSession, error) {
	var out []bfSession

	actRows, err := tx.QueryContext(ctx, `
		SELECT a.id, a.user_id, a.start_time, a.created_at
		FROM activities a
		WHERE a.activity_type = 'running' AND a.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM planned_workouts p
		    WHERE p.completed_session_kind = 'activity' AND p.completed_session_id = a.id
		  )
	`)
	if err != nil {
		return nil, err
	}
	for actRows.Next() {
		var s bfSession
		s.kind = "activity"
		if err := actRows.Scan(&s.id, &s.userID, &s.startUTC, &s.createdAt); err != nil {
			actRows.Close()
			return nil, err
		}
		out = append(out, s)
	}
	if err := actRows.Err(); err != nil {
		actRows.Close()
		return nil, err
	}
	actRows.Close()

	woRows, err := tx.QueryContext(ctx, `
		SELECT w.id, w.user_id, w.performed_at, w.created_at
		FROM workouts w
		WHERE w.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM planned_workouts p
		    WHERE p.completed_session_kind = 'workout' AND p.completed_session_id = w.id
		  )
	`)
	if err != nil {
		return nil, err
	}
	for woRows.Next() {
		var s bfSession
		s.kind = "workout"
		if err := woRows.Scan(&s.id, &s.userID, &s.startUTC, &s.createdAt); err != nil {
			woRows.Close()
			return nil, err
		}
		out = append(out, s)
	}
	if err := woRows.Err(); err != nil {
		woRows.Close()
		return nil, err
	}
	woRows.Close()

	return out, nil
}

// bfSelectPlan is the FROZEN copy of selectPlan: planned-status plans of the
// matching kind, same local calendar day in the plan's own timezone, nearest
// scheduled start (ties: earliest start, then smallest id).
func bfSelectPlan(ctx context.Context, tx *sql.Tx, s bfSession, wantKind string) (*bfPlan, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, activity_kind, scheduled_start_utc, timezone
		FROM planned_workouts
		WHERE user_id = ? AND status = 'planned' AND activity_kind = ? AND deleted_at IS NULL
	`, s.userID, wantKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var best *bfPlan
	var bestDelta time.Duration
	for rows.Next() {
		var p bfPlan
		if err := rows.Scan(&p.id, &p.activityKind, &p.startUTC, &p.timezone); err != nil {
			return nil, err
		}
		if !bfSameLocalDay(p, s.startUTC) {
			continue
		}
		delta := p.startUTC.Sub(s.startUTC)
		if delta < 0 {
			delta = -delta
		}
		if best == nil || delta < bestDelta ||
			(delta == bestDelta && (p.startUTC.Before(best.startUTC) ||
				(p.startUTC.Equal(best.startUTC) && p.id < best.id))) {
			b := p
			best = &b
			bestDelta = delta
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return best, nil
}

func bfSameLocalDay(p bfPlan, sessionStartUTC time.Time) bool {
	loc, err := time.LoadLocation(p.timezone)
	if err != nil {
		return false
	}
	ps := p.startUTC.In(loc)
	ss := sessionStartUTC.In(loc)
	py, pm, pd := ps.Date()
	sy, sm, sd := ss.Date()
	return py == sy && pm == sm && pd == sd
}
