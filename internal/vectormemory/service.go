package vectormemory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
)

// Service orchestrates retrieval (the shared path for the agent endpoint
// and the admin probe) and per-session distillation (used by the job and
// backfill). It logs latency + outcomes around the paid provider calls.
//
// This is the single retrieval/distillation code path: the agent's internal
// endpoint, the admin probe, the distillation job, and the backfill all call
// through here. Observability on the paid embed/distill calls lives in this
// service because the providers are intentionally logger-free.
type Service struct {
	repo      Repository
	embedder  Embedder
	distiller Distiller
	cfg       config.VectorMemoryConfig
	log       *slog.Logger
	now       func() time.Time
}

// NewService wires the service. now defaults to time.Now so created_at is
// stamped consistently with the repo's UTC normalization; tests can't override
// it but don't need to (created_at is not asserted on).
func NewService(repo Repository, embedder Embedder, distiller Distiller, cfg config.VectorMemoryConfig, log *slog.Logger) *Service {
	return &Service{
		repo:      repo,
		embedder:  embedder,
		distiller: distiller,
		cfg:       cfg,
		log:       log,
		now:       time.Now,
	}
}

// ConversationMessage is one turn the distiller reads. Role is "user" or
// "assistant"; Content is the message text.
//
// why: the service takes this shape rather than chat.Message so it does not
// import the chat package — that would create an import cycle. The job and
// backfill adapt chat.Message to this.
type ConversationMessage struct {
	Role    string
	Content string
}

// Retrieve is THE retrieval path shared by the agent endpoint and the admin
// probe: it embeds the query and returns the nearest non-superseded memories
// for the active embedding model, ordered ascending by distance.
//
// Threshold sentinel semantics (so the probe can sweep the full neighborhood
// while the agent applies the tuned relevance gate):
//   - threshold < 0  ⇒ use the config default (cfg.DistanceThreshold). This is
//     the agent's normal call: -1 means "whatever the tuned cap is".
//   - threshold == 0 ⇒ no distance cap at all (full sweep). The admin probe
//     uses this to see every neighbor regardless of distance.
//   - threshold > 0  ⇒ cap at exactly that value.
//
// The resolved cap is passed to repo.Search, whose maxDistance <= 0 already
// means "no cap" — so a resolved 0 (full sweep) maps cleanly onto that.
func (s *Service) Retrieve(ctx context.Context, userID, query string, k int, threshold float64) ([]Match, error) {
	if query == "" {
		return nil, nil
	}
	if k <= 0 {
		k = s.cfg.TopK
	}

	// Resolve the threshold sentinel. A negative threshold defers to the
	// configured default; 0 stays 0 (no cap); a positive value is used as-is.
	resolvedCap := threshold
	if threshold < 0 {
		resolvedCap = s.cfg.DistanceThreshold
	}

	embedStart := s.now()
	// Retrieve is the synchronous agent/probe path, not the distillation job —
	// its embed usage is intentionally not metered here (the cost dashboard
	// scopes to the background goroutine).
	vecs, _, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("vectormemory: embed query: %w", err)
	}
	embedLatency := s.now().Sub(embedStart)
	s.log.DebugContext(ctx, "vectormemory embedded query",
		slog.Duration("latency", embedLatency),
		slog.Int("vectors", len(vecs)),
	)
	if len(vecs) == 0 {
		return nil, nil
	}

	matches, err := s.repo.Search(ctx, userID, s.cfg.EmbedModel, vecs[0], k, resolvedCap)
	if err != nil {
		return nil, fmt.Errorf("vectormemory: search: %w", err)
	}
	s.log.InfoContext(ctx, "vectormemory retrieve",
		slog.String("user_id", userID),
		slog.Int("k", k),
		slog.Float64("threshold", resolvedCap),
		slog.Int("matches", len(matches)),
		slog.Int64("embed_latency_ms", embedLatency.Milliseconds()),
	)
	return matches, nil
}

// DistillSession distills one session's conversation into durable observations
// and persists them, returning the number actually inserted. It is the path
// the background job and the backfill share.
//
// Per-observation insert-failure policy: a single failed insert does NOT abort
// the whole session — it is logged at warn and the loop continues to the next
// observation. The method returns the count actually inserted with a nil error
// even if some inserts failed. why: one bad row (e.g. a transient constraint
// hiccup) should not throw away the rest of a hard-won, paid distillation; the
// job re-runs idempotently anyway and dedup catches re-inserts next time.
func (s *Service) DistillSession(ctx context.Context, userID, sessionID string, messages []ConversationMessage) (int, error) {
	rendered := renderConversation(messages)

	distillStart := s.now()
	observations, distillUsage, err := s.distiller.Distill(ctx, rendered)
	distillDuration.Observe(s.now().Sub(distillStart).Seconds())
	if err != nil {
		stageErrorsTotal.WithLabelValues("distill").Inc()
		return 0, fmt.Errorf("vectormemory: distill session: %w", err)
	}
	// Token spend is recorded on success only — a failed call's usage is
	// unreliable and the error counter already marks the wasted attempt.
	distillTokensTotal.WithLabelValues("input").Add(float64(distillUsage.InputTokens))
	distillTokensTotal.WithLabelValues("output").Add(float64(distillUsage.OutputTokens))
	observationsDistilledTotal.Add(float64(len(observations)))
	s.log.InfoContext(ctx, "vectormemory distilled session",
		slog.String("user_id", userID),
		slog.String("session_id", sessionID),
		slog.Duration("latency", s.now().Sub(distillStart)),
		slog.Int("observations", len(observations)),
	)
	if len(observations) == 0 {
		return 0, nil
	}

	embedStart := s.now()
	vecs, embedUsage, err := s.embedder.Embed(ctx, observations)
	embedDuration.Observe(s.now().Sub(embedStart).Seconds())
	if err != nil {
		stageErrorsTotal.WithLabelValues("embed").Inc()
		return 0, fmt.Errorf("vectormemory: embed observations: %w", err)
	}
	embedTokensTotal.Add(float64(embedUsage.TotalTokens))
	s.log.DebugContext(ctx, "vectormemory embedded observations",
		slog.Duration("latency", s.now().Sub(embedStart)),
		slog.Int("vectors", len(vecs)),
	)
	if len(vecs) != len(observations) {
		return 0, fmt.Errorf("vectormemory: embed returned %d vectors for %d observations", len(vecs), len(observations))
	}

	dedupEnabled := s.cfg.DedupThreshold > 0
	if !dedupEnabled {
		// Log once per call rather than once per observation: an un-tuned
		// DedupThreshold (0) means dedup is intentionally off, so we insert
		// everything and skip the NearestDistance probe entirely.
		s.log.DebugContext(ctx, "vectormemory dedup disabled (DedupThreshold == 0)",
			slog.String("session_id", sessionID),
		)
	}

	inserted := 0
	for i, obs := range observations {
		vec := vecs[i]

		if dedupEnabled {
			dist, found, err := s.repo.NearestDistance(ctx, userID, s.cfg.EmbedModel, vec)
			if err != nil {
				stageErrorsTotal.WithLabelValues("dedup").Inc()
				return inserted, fmt.Errorf("vectormemory: dedup probe: %w", err)
			}
			if found && dist <= s.cfg.DedupThreshold {
				observationsDedupedTotal.Inc()
				s.log.DebugContext(ctx, "vectormemory skipping near-duplicate",
					slog.String("user_id", userID),
					slog.String("session_id", sessionID),
					slog.Float64("distance", dist),
				)
				continue
			}
		}

		// SourceMessageID is left nil: message-level attribution is best-effort
		// and not wired in v1 — the distiller fuses multiple turns into one
		// observation, so there is no single message to attribute it to.
		// A local copy makes &srcSessionID safe to take per iteration.
		srcSessionID := sessionID
		if _, err := s.repo.Insert(ctx, NewMemory{
			UserID:          userID,
			DistilledText:   obs,
			SourceType:      "chat_session",
			SourceSessionID: &srcSessionID,
			EmbeddingModel:  s.cfg.EmbedModel,
			EmbeddingDim:    s.cfg.EmbedDim,
			Embedding:       vec,
			CreatedAt:       s.now().UTC(),
		}); err != nil {
			// Continue rather than abort: see the method's insert-failure policy.
			stageErrorsTotal.WithLabelValues("insert").Inc()
			s.log.WarnContext(ctx, "vectormemory insert failed, skipping observation",
				slog.String("user_id", userID),
				slog.String("session_id", sessionID),
				slog.Any("error", err),
			)
			continue
		}
		observationsInsertedTotal.Inc()
		inserted++
	}

	s.log.InfoContext(ctx, "vectormemory distill session persisted",
		slog.String("user_id", userID),
		slog.String("session_id", sessionID),
		slog.Int("inserted", inserted),
	)
	return inserted, nil
}

// Dump is a thin passthrough to repo.Dump so the admin handler (Task 6) stays
// off the repository and goes through the single service seam.
func (s *Service) Dump(ctx context.Context, userID string, limit, offset int) ([]Memory, error) {
	return s.repo.Dump(ctx, userID, limit, offset)
}

// DefaultThreshold is the configured distance cap the agent path applies when a
// caller omits a threshold (i.e. passes the -1 sentinel). The admin search
// handler echoes this back so an operator can see the active cap without a
// config round-trip.
func (s *Service) DefaultThreshold() float64 {
	return s.cfg.DistanceThreshold
}

// renderConversation flattens turns into the plain transcript the distiller
// reads, one "role: content" line per turn.
func renderConversation(messages []ConversationMessage) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}
