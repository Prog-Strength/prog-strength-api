package vectormemory

import (
	"context"
	"log/slog"
	"time"
)

// distillTickInterval is how often runDistill wakes up to sweep for newly
// settled, undistilled units across every registered source. Five minutes
// keeps freshly-quiet units from waiting long while staying well clear of the
// paid provider budget (each tick processes at most distillBatchSize units per
// source).
const distillTickInterval = 5 * time.Minute

// distillBatchSize bounds how many units one tick processes per source, capping
// the embed/distill spend per wake-up. Settled units that don't fit in a batch
// are picked up on the next tick (oldest-settled first).
const distillBatchSize = 20

// MemorySource is one origin of unstructured signal to distill into memories.
// One implementation per source type; all are held in a registry the job
// ranges over. Implementations live consumer-side (package server) so
// vectormemory never imports chat or workout.
type MemorySource interface {
	// SourceType is the stable discriminator stored on every memory this
	// source produces, e.g. "chat_session" or "workout_note".
	SourceType() string

	// PendingUnits returns units that have settled (gone idle past this
	// source's own window, relative to now) and are not yet distilled, up to
	// limit, oldest-settled first.
	PendingUnits(ctx context.Context, now time.Time, limit int) ([]DistillUnit, error)

	// CountPending returns the full, un-capped settled-and-undistilled backlog
	// for the same window PendingUnits selects against — feeds the idle backlog
	// gauge, which the capped PendingUnits cannot.
	//
	// CountPending is a deliberate, documented extension of the SOW's 4-method
	// sketch: it exists only to preserve the existing
	// api_vectormemory_idle_sessions backlog gauge (PR #59), which PendingUnits
	// cannot feed because it is capped at limit. The gauge becomes the SUM of
	// CountPending across sources, keeping the metric name/shape (and the
	// Grafana dashboard) intact.
	CountPending(ctx context.Context, now time.Time) (int, error)

	// AllUndistilled returns a page of not-yet-distilled units, ignoring the
	// settle window. Used only by the one-time backfill; cursor-paginated,
	// returning the next cursor ("" when exhausted).
	AllUndistilled(ctx context.Context, cursor string, limit int) ([]DistillUnit, string, error)

	// MarkDistilled records that a unit was processed (even with zero
	// observations) so it isn't re-examined.
	MarkDistilled(ctx context.Context, unitID string, at time.Time) error
}

// DistillUnit is one self-contained unit of content ready to distill.
type DistillUnit struct {
	UnitID     string // source-local id (chat session id, workout id)
	UserID     string
	Content    string     // the assembled text handed to the distiller
	PromptHint string     // source-specific framing appended to the distiller prompt
	Source     Provenance // which typed FK column(s) the resulting memory fills
}

// Provenance names the origin so the repository writes the right typed FK +
// discriminator.
type Provenance struct {
	SourceType string  // "chat_session" | "workout_note"
	SessionID  *string // set iff SourceType == "chat_session"
	MessageID  *int64  // best-effort, chat only
	WorkoutID  *string // set iff SourceType == "workout_note"
}

// StartDistillation launches the background distillation loop in a
// goroutine and returns immediately; the goroutine runs until ctx is
// canceled. It is a no-op (logged) when the feature is disabled or the
// paid providers aren't configured — server.go also gates on Enabled, so
// this is defensive belt-and-suspenders that keeps the loop from spending
// against unconfigured providers.
func (s *Service) StartDistillation(ctx context.Context, sources []MemorySource) {
	if !s.cfg.Enabled {
		s.log.InfoContext(ctx, "vectormemory distillation job not started: feature disabled")
		return
	}
	if !s.embedder.Configured() || !s.distiller.Configured() {
		s.log.InfoContext(ctx, "vectormemory distillation job not started: providers not configured")
		return
	}
	go s.runDistill(ctx, sources)
}

func (s *Service) runDistill(ctx context.Context, sources []MemorySource) {
	// Run once at start so a freshly-started process drains the existing
	// backlog rather than waiting a full tick. distillOnce already logs its own
	// per-unit outcomes; the batch-level error is logged inside it too.
	if err := s.distillOnce(ctx, sources); err != nil {
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
			if err := s.distillOnce(ctx, sources); err != nil {
				s.log.WarnContext(ctx, "vectormemory distillation: sweep failed",
					slog.Any("error", err),
				)
			}
		}
	}
}

// distillOnce processes one batch of settled, undistilled units per source.
// Per-unit failures are logged and skipped so one bad unit never aborts the
// batch; a source whose PendingUnits errors is logged as a stage error and the
// loop continues to the next source (it does NOT abort the whole sweep).
//
// Mark-on-success-only retry policy: a unit is stamped distilled ONLY after
// DistillUnit succeeds (zero observations is success — the content simply held
// nothing durable, and re-distilling it would just re-spend for the same empty
// result). If the distiller errors, the unit is left unmarked so it's retried
// on the next tick. The mark write itself failing is logged but not retried
// inline (the unit reappears in the next sweep and re-distills idempotently —
// dedup catches the re-inserts).
func (s *Service) distillOnce(ctx context.Context, sources []MemorySource) error {
	// One clock read per sweep keeps the cutoff and the mark timestamps on a
	// single consistent moment rather than drifting across the loop.
	now := s.now()

	// last_sweep_timestamp marks "the loop is executing" — stamped at the end
	// of every attempt, so its absence is the dead-goroutine signal. Sweep
	// duration is timed across the whole tick.
	start := now
	defer func() {
		end := s.now()
		sweepDuration.Observe(end.Sub(start).Seconds())
		lastSweepTimestamp.Set(float64(end.Unix()))
	}()

	// backlog accumulates the full, un-capped settled-and-undistilled count
	// across every source; idleSessions (the historical backlog gauge name) is
	// set to that sum once the loop completes. countOK records whether at least
	// one source's count succeeded this tick: if every CountPending failed we
	// leave the gauge untouched (hold its previous value) rather than writing a
	// false 0 that would read as "backlog drained".
	backlog := 0
	countOK := false
	selected, distilled := 0, 0
	sawStageError := false
	for _, src := range sources {
		// A count failure is non-fatal — it only feeds a gauge — so it is
		// logged and the running sum left untouched for this source rather than
		// aborting the sweep.
		if n, err := src.CountPending(ctx, now); err != nil {
			s.log.WarnContext(ctx, "vectormemory distillation: count pending failed",
				slog.String("source_type", src.SourceType()),
				slog.Any("error", err),
			)
		} else {
			backlog += n
			countOK = true
		}

		units, err := src.PendingUnits(ctx, now, distillBatchSize)
		if err != nil {
			// One source's select failure must not abort the others, so it is a
			// stage error (classifies the sweep "partial") and the loop
			// continues to the next source.
			stageErrorsTotal.WithLabelValues("select").Inc()
			sawStageError = true
			s.log.ErrorContext(ctx, "vectormemory distillation: select pending units failed",
				slog.String("source_type", src.SourceType()),
				slog.Any("error", err),
			)
			continue
		}
		selected += len(units)
		sessionsSelectedTotal.Add(float64(len(units)))

		for _, unit := range units {
			if _, err := s.DistillUnit(ctx, unit); err != nil {
				// DistillUnit already incremented the specific stage error
				// (distill/embed/dedup). Leave unmarked so the next sweep
				// retries the paid distillation.
				sawStageError = true
				s.log.WarnContext(ctx, "vectormemory distillation: distill unit failed, leaving unmarked for retry",
					slog.String("source_type", src.SourceType()),
					slog.String("unit_id", unit.UnitID),
					slog.String("user_id", unit.UserID),
					slog.Any("error", err),
				)
				continue
			}

			// Success (including the zero-observation case): stamp it so it
			// stops showing up in PendingUnits.
			if err := src.MarkDistilled(ctx, unit.UnitID, now); err != nil {
				stageErrorsTotal.WithLabelValues("mark").Inc()
				sawStageError = true
				s.log.WarnContext(ctx, "vectormemory distillation: mark distilled failed",
					slog.String("source_type", src.SourceType()),
					slog.String("unit_id", unit.UnitID),
					slog.String("user_id", unit.UserID),
					slog.Any("error", err),
				)
				continue
			}
			sessionsDistilledTotal.Inc()
			distilled++
		}
	}
	// Hold the gauge on a total count failure rather than reporting a false 0:
	// only write when at least one source's CountPending succeeded this tick, so
	// a transient count error preserves the previous backlog value instead of
	// dipping to a misleading "backlog drained" 0.
	if countOK {
		idleSessions.Set(float64(backlog))
	}

	// Every source's select succeeded-or-was-skipped, so the loop completed and
	// last_success advances even when no units were selected. The result is
	// "partial" if any unit/source hit a stage error, "success" otherwise. The
	// sweepsTotal{result="error"} label value is retained for dashboard
	// stability but is no longer emitted: a single source's select failure is a
	// stage error rather than a batch abort.
	lastSuccessTimestamp.Set(float64(s.now().Unix()))
	result := "success"
	if sawStageError {
		result = "partial"
	}
	sweepsTotal.WithLabelValues(result).Inc()

	// One summary line per sweep so the paid loop is observable (how many of
	// the selected units were distilled this tick).
	s.log.InfoContext(ctx, "vectormemory distillation: sweep complete",
		slog.Int("selected", selected),
		slog.Int("distilled", distilled),
		slog.String("result", result),
	)
	return nil
}
