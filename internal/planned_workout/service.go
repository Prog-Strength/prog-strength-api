package plannedworkout

import (
	"context"
	"errors"
	"log"
	"time"
)

// Service owns the single completion code path shared by the HTTP complete
// handler and the auto-matcher, plus the unlink inverse. Google Calendar
// rewrites are best-effort: a Google failure is logged, never returned.
type Service struct {
	repo     Repository
	calendar CalendarScheduler // may be nil when calendar sync is unconfigured
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) SetCalendar(c CalendarScheduler) { s.calendar = c }

func (s *Service) LinkCompletion(ctx context.Context, userID, planID, sessionID string, kind SessionKind) (*PlannedWorkout, error) {
	plan, err := s.repo.Get(ctx, userID, planID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetCompletion(ctx, userID, planID, sessionID, kind); err != nil {
		return nil, err
	}
	if plan.GoogleEventID != nil && *plan.GoogleEventID != "" && s.calendar != nil {
		actualText := "Completed — logged " + string(kind) + " session " + sessionID
		if err := s.calendar.RewriteCompleted(ctx, userID, planID, actualText); err != nil {
			log.Printf("planned-workout link completion: rewrite google event (plan %s): %v", planID, err)
		}
	}
	return s.repo.Get(ctx, userID, planID)
}

// OnSessionLogged best-effort links a freshly logged session to the planned
// workout it completes, if any. A no-candidate result is a clean no-op; all
// failures are logged, never returned (matching must not fail ingest).
func (s *Service) OnSessionLogged(ctx context.Context, userID, sessionID string, kind SessionKind, sessionStartUTC time.Time) {
	since := sessionStartUTC.Add(-36 * time.Hour)
	until := sessionStartUTC.Add(36 * time.Hour)
	plans, err := s.repo.List(ctx, userID, &since, &until)
	if err != nil {
		log.Printf("plan matcher: list candidates (user %s): %v", userID, err)
		return
	}
	match := selectPlan(plans, sessionStartUTC, kind)
	if match == nil {
		return
	}
	if _, err := s.LinkCompletion(ctx, userID, match.ID, sessionID, kind); err != nil {
		log.Printf("plan matcher: link completion (plan %s, session %s): %v", match.ID, sessionID, err)
	}
}

// OnSessionDeleted reverts the plan (if any) whose completion link points at the
// deleted session back to planned, clearing the link and re-rendering the Google
// event. Best-effort; a no-link session is a clean no-op.
func (s *Service) OnSessionDeleted(ctx context.Context, userID, sessionID string, kind SessionKind) {
	plan, err := s.repo.GetByCompletedSession(ctx, userID, sessionID, kind)
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
