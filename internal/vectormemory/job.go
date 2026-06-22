package vectormemory

import (
	"context"
	"log/slog"
	"time"
)

// distillTickInterval is how often runDistill wakes up to sweep for newly
// idle, undistilled sessions. Five minutes keeps freshly-quiet sessions
// from waiting long while staying well clear of the paid provider budget
// (each tick processes at most distillBatchSize sessions).
const distillTickInterval = 5 * time.Minute

// distillBatchSize bounds how many sessions one tick processes, capping the
// embed/distill spend per wake-up. Idle sessions that don't fit in a batch
// are picked up on the next tick (oldest-idle first).
const distillBatchSize = 20

// SessionSource is the slice of chat storage the distillation job needs.
// Defined here (not imported from chat) so vectormemory doesn't depend on
// the chat package; server.go adapts *chat.SQLiteRepository to it.
type SessionSource interface {
	IdleUndistilled(ctx context.Context, cutoff time.Time, limit int) ([]IdleSession, error)
	// CountIdleUndistilled returns the full, un-capped backlog for the same
	// cutoff IdleUndistilled selects against — the idle_sessions gauge.
	CountIdleUndistilled(ctx context.Context, cutoff time.Time) (int, error)
	Conversation(ctx context.Context, sessionID string) ([]ConversationMessage, error)
	MarkDistilled(ctx context.Context, sessionID string, at time.Time) error
}

// IdleSession is the minimal session identity the job carries from
// selection through distillation: the session id plus its owning user
// (the job runs cross-user, so it must forward the user to DistillSession).
type IdleSession struct {
	ID     string
	UserID string
}

// StartDistillation launches the background distillation loop in a
// goroutine and returns immediately; the goroutine runs until ctx is
// canceled. It is a no-op (logged) when the feature is disabled or the
// paid providers aren't configured — server.go also gates on Enabled, so
// this is defensive belt-and-suspenders that keeps the loop from spending
// against unconfigured providers.
func (s *Service) StartDistillation(ctx context.Context, src SessionSource) {
	if !s.cfg.Enabled {
		s.log.InfoContext(ctx, "vectormemory distillation job not started: feature disabled")
		return
	}
	if !s.embedder.Configured() || !s.distiller.Configured() {
		s.log.InfoContext(ctx, "vectormemory distillation job not started: providers not configured")
		return
	}
	go s.runDistill(ctx, src)
}

func (s *Service) runDistill(ctx context.Context, src SessionSource) {
	// Run once at start so a freshly-started process drains the existing
	// idle backlog rather than waiting a full tick. distillOnce already
	// logs its own per-session outcomes; the batch-level error is logged
	// inside it too.
	if err := s.distillOnce(ctx, src); err != nil {
		s.log.WarnContext(ctx, "vectormemory distillation: initial sweep failed",
			slog.Any("error", err),
		)
	}

	ticker := time.NewTicker(distillTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.distillOnce(ctx, src); err != nil {
				s.log.WarnContext(ctx, "vectormemory distillation: sweep failed",
					slog.Any("error", err),
				)
			}
		}
	}
}

// distillOnce processes one batch of idle, undistilled sessions. Per-session
// failures are logged and skipped so one bad session never aborts the batch;
// only a failure to select the batch itself is returned.
//
// Mark-on-success-only retry policy: a session is stamped distilled ONLY
// after DistillSession succeeds (zero observations is success — the
// conversation simply held nothing durable, and re-distilling it would just
// re-spend for the same empty result). If reading the conversation fails or
// the distiller errors, the session is left unmarked so it's retried on the
// next tick. The mark write itself failing is logged but not retried inline
// (the session reappears in the next sweep and re-distills idempotently —
// dedup catches the re-inserts).
func (s *Service) distillOnce(ctx context.Context, src SessionSource) error {
	// One clock read per sweep keeps the cutoff and the mark timestamps on a
	// single consistent moment rather than drifting across the loop.
	now := s.now()
	cutoff := now.Add(-time.Duration(s.cfg.SessionIdleMinutes) * time.Minute)

	// last_sweep_timestamp marks "the loop is executing" — stamped at the end
	// of every attempt, success or batch error, so its absence is the
	// dead-goroutine signal. Sweep duration is timed across the whole tick.
	start := now
	defer func() {
		end := s.now()
		sweepDuration.Observe(end.Sub(start).Seconds())
		lastSweepTimestamp.Set(float64(end.Unix()))
	}()

	// Sample the full backlog before processing this batch. A count failure is
	// non-fatal — it only feeds a gauge — so it is logged and the gauge left
	// untouched rather than aborting the sweep.
	if backlog, err := src.CountIdleUndistilled(ctx, cutoff); err != nil {
		s.log.WarnContext(ctx, "vectormemory distillation: count idle sessions failed",
			slog.Any("error", err),
		)
	} else {
		idleSessions.Set(float64(backlog))
	}

	sessions, err := src.IdleUndistilled(ctx, cutoff, distillBatchSize)
	if err != nil {
		stageErrorsTotal.WithLabelValues("select").Inc()
		sweepsTotal.WithLabelValues("error").Inc()
		s.log.ErrorContext(ctx, "vectormemory distillation: select idle sessions failed",
			slog.Any("error", err),
		)
		return err
	}
	sessionsSelectedTotal.Add(float64(len(sessions)))

	// sawStageError tracks whether any session hit a per-stage failure, which
	// classifies an otherwise-complete sweep as "partial" rather than
	// "success". Per-observation insert loss is counted inside DistillSession
	// (it returns nil per the insert-failure policy) and surfaces via the
	// observation-counter gap instead.
	distilled := 0
	sawStageError := false
	for _, sess := range sessions {
		msgs, err := src.Conversation(ctx, sess.ID)
		if err != nil {
			// Leave unmarked so the next sweep retries reading it.
			stageErrorsTotal.WithLabelValues("load").Inc()
			sawStageError = true
			s.log.WarnContext(ctx, "vectormemory distillation: load conversation failed, skipping",
				slog.String("session_id", sess.ID),
				slog.String("user_id", sess.UserID),
				slog.Any("error", err),
			)
			continue
		}

		if _, err := s.DistillSession(ctx, sess.UserID, sess.ID, msgs); err != nil {
			// DistillSession already incremented the specific stage error
			// (distill/embed/dedup). Leave unmarked so the next sweep retries
			// the paid distillation.
			sawStageError = true
			s.log.WarnContext(ctx, "vectormemory distillation: distill session failed, leaving unmarked for retry",
				slog.String("session_id", sess.ID),
				slog.String("user_id", sess.UserID),
				slog.Any("error", err),
			)
			continue
		}

		// Success (including the zero-observation case): stamp it so it
		// stops showing up in IdleUndistilled.
		if err := src.MarkDistilled(ctx, sess.ID, now); err != nil {
			stageErrorsTotal.WithLabelValues("mark").Inc()
			sawStageError = true
			s.log.WarnContext(ctx, "vectormemory distillation: mark distilled failed",
				slog.String("session_id", sess.ID),
				slog.String("user_id", sess.UserID),
				slog.Any("error", err),
			)
			continue
		}
		sessionsDistilledTotal.Inc()
		distilled++
	}

	// The batch select succeeded, so the loop completed and last_success
	// advances even when no sessions were selected. The result is "partial" if
	// any session hit a stage error, "success" otherwise.
	lastSuccessTimestamp.Set(float64(s.now().Unix()))
	result := "success"
	if sawStageError {
		result = "partial"
	}
	sweepsTotal.WithLabelValues(result).Inc()

	// One summary line per sweep so the paid loop is observable (how many of
	// the selected batch were distilled this tick).
	s.log.InfoContext(ctx, "vectormemory distillation: sweep complete",
		slog.Int("selected", len(sessions)),
		slog.Int("distilled", distilled),
		slog.String("result", result),
	)
	return nil
}
