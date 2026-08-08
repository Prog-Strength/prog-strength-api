package plannedworkout

import (
	"context"
	"errors"
	"log"
	"time"
)

// ActivityKindResolver resolves the plan ActivityKind that a logged session
// completes by looking up the session's activity_type in the unified activities
// base table (strength_training → lift, endurance → run). Implemented in server
// wiring over the activities repo so this package keeps its zero dependency on
// internal/activity. Nil-safe at the call site: auto-matching no-ops without it.
type ActivityKindResolver interface {
	ResolvePlanKind(ctx context.Context, userID, sessionID string) (ActivityKind, error)
}

// Service owns the single completion code path shared by the HTTP complete
// handler and the auto-matcher, plus the unlink inverse. Google Calendar
// rewrites are best-effort: a Google failure is logged, never returned.
type Service struct {
	repo     Repository
	calendar CalendarScheduler    // may be nil when calendar sync is unconfigured
	kinds    ActivityKindResolver // may be nil; auto-matching then no-ops
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) SetCalendar(c CalendarScheduler) { s.calendar = c }

// SetKindResolver wires the activity-kind resolver the auto-matcher uses to
// route a logged session to the plan kind (lift/run) it completes. Injected
// from server wiring after construction, mirroring SetCalendar.
func (s *Service) SetKindResolver(r ActivityKindResolver) { s.kinds = r }

func (s *Service) LinkCompletion(ctx context.Context, userID, planID, sessionID string) (*PlannedWorkout, error) {
	// Existence/ownership check before the write, so a bad plan id surfaces
	// as this repository's not-found rather than as a silent no-op update.
	// The plan body itself is no longer needed here: the completed event is
	// rendered from the ACTIVITY now, not from the plan's stored agenda.
	if _, err := s.repo.Get(ctx, userID, planID); err != nil {
		return nil, err
	}
	if err := s.repo.SetCompletion(ctx, userID, planID, sessionID); err != nil {
		return nil, err
	}
	// Hand the event to the session that completed it. Note this no longer
	// requires the plan to ALREADY have a Google event: an activity that
	// completes an unsynced plan still syncs on its own merits, and letting
	// the takeover run unconditionally keeps one code path instead of two.
	if s.calendar != nil {
		if err := s.calendar.SyncCompletedActivity(ctx, userID, planID, sessionID); err != nil {
			log.Printf("planned-workout link completion: sync completed activity (plan %s): %v", planID, err)
		}
	}
	return s.repo.Get(ctx, userID, planID)
}

// OnSessionLogged best-effort links a freshly logged session to the planned
// workout it completes, if any. The plan kind the session can complete is
// derived from the session's own activity_type via the resolver, so a strength
// session completes a lift plan and an endurance session a run plan. A
// no-candidate result is a clean no-op; all failures are logged, never returned
// (matching must not fail ingest).
func (s *Service) OnSessionLogged(ctx context.Context, userID, sessionID string, sessionStartUTC time.Time) {
	if s.kinds == nil {
		return
	}
	wantKind, err := s.kinds.ResolvePlanKind(ctx, userID, sessionID)
	if err != nil {
		log.Printf("plan matcher: resolve activity kind (session %s): %v", sessionID, err)
		return
	}
	since := sessionStartUTC.Add(-36 * time.Hour)
	until := sessionStartUTC.Add(36 * time.Hour)
	plans, err := s.repo.List(ctx, userID, &since, &until)
	if err != nil {
		log.Printf("plan matcher: list candidates (user %s): %v", userID, err)
		return
	}
	match := selectPlan(plans, sessionStartUTC, wantKind)
	if match == nil {
		return
	}
	if _, err := s.LinkCompletion(ctx, userID, match.ID, sessionID); err != nil {
		log.Printf("plan matcher: link completion (plan %s, session %s): %v", match.ID, sessionID, err)
	}
}

// OnSessionDeleted reverts the plan (if any) whose completion link points at the
// deleted session back to planned, clearing the link and re-rendering the Google
// event. Best-effort; a no-link session is a clean no-op. The lookup is keyed by
// session id alone (see Repository.GetByCompletedSession).
func (s *Service) OnSessionDeleted(ctx context.Context, userID, sessionID string) {
	plan, err := s.repo.GetByCompletedSession(ctx, userID, sessionID)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err != nil {
		log.Printf("plan matcher: reverse lookup (session %s): %v", sessionID, err)
		return
	}
	if _, err := s.Unlink(ctx, userID, plan.ID); err != nil {
		log.Printf("plan matcher: revert on delete (plan %s): %v", plan.ID, err)
	}
}

func (s *Service) Unlink(ctx context.Context, userID, planID string) (*PlannedWorkout, error) {
	plan, err := s.repo.Get(ctx, userID, planID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.ClearCompletion(ctx, userID, planID); err != nil {
		return nil, err
	}
	if plan.GoogleEventID != nil && *plan.GoogleEventID != "" && s.calendar != nil {
		if err := s.calendar.Resync(ctx, userID, planID); err != nil {
			log.Printf("planned-workout unlink: re-render google event (plan %s): %v", planID, err)
		}
	}
	return s.repo.Get(ctx, userID, planID)
}
