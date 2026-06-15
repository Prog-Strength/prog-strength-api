package plannedworkout

import (
	"context"
	"log"
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
