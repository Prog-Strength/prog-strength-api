// Command memory-backfill is a ONE-TIME, MANUAL-RUN tool that seeds the agent
// vector-memory index from historical chat conversations. It is NOT invoked
// automatically anywhere — no cron, no server hook, no background job. Run it
// by hand exactly once (or re-run safely: it is idempotent — already-distilled
// sessions are skipped via the memory_distilled_at IS NULL filter).
//
// why batch APIs: this is a single, large, latency-insensitive job, so it uses
// the half-price async Batch APIs (vectormemory.BatchDistiller +
// vectormemory.BatchEmbedder) — all historical conversations in one Anthropic
// Message Batch, all resulting observations in one OpenAI embeddings Batch.
// Both calls BLOCK until their batch completes (minutes-to-hours), which is the
// intended behavior for a run-once seeding tool.
//
// Cost visibility: a startup line states how many sessions/requests will be
// submitted before any paid call is made. Counts are logged liberally
// throughout (sessions found, observations produced, memories inserted,
// sessions marked).
//
// --dry-run distills + embeds and prints the counts WITHOUT writing any memory
// rows or marking any session distilled. It still spends on the batch APIs (it
// has to, to produce the counts), so it is a "preview the result, change
// nothing" switch rather than a "spend nothing" one.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata"

	progstrength "github.com/jwallace145/progressive-overload-fitness-tracker"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/chat"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// backfillHTTPTimeout bounds each individual HTTP call (upload, create, one
// poll, one download) — NOT the whole batch, which spans many polls. Batch
// jobs run for a long wall-clock time, but every single request is quick.
const backfillHTTPTimeout = 60 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dryRun := flag.Bool("dry-run", false, "distill + embed and print counts without writing memories or marking sessions")
	flag.Parse()

	if err := run(ctx, *dryRun); err != nil {
		log.Fatalf("memory-backfill: %v", err)
	}
}

func run(ctx context.Context, dryRun bool) error {
	cfg, err := config.Load(progstrength.DefaultConfigTOML)
	if err != nil {
		return err
	}

	// Backfill runs regardless of cfg.VectorMemory.Enabled — that is the
	// retrieval kill-switch, and seeding the index does not change live
	// behavior. But it needs both paid providers, so fail loudly if either
	// key is missing.
	if cfg.VectorMemory.OpenAIAPIKey == "" {
		return errors.New("openai api key not configured (vectormemory.OpenAIAPIKey)")
	}
	if cfg.VectorMemory.AnthropicAPIKey == "" {
		return errors.New("anthropic api key not configured (vectormemory.AnthropicAPIKey)")
	}

	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	if err := db.Migrate(database); err != nil {
		return err
	}

	client := &http.Client{Timeout: backfillHTTPTimeout}
	distiller := vectormemory.NewBatchDistiller(client, cfg.VectorMemory.AnthropicAPIKey, cfg.VectorMemory.DistillModel)
	embedder := vectormemory.NewBatchEmbedder(client, cfg.VectorMemory.OpenAIAPIKey, cfg.VectorMemory.EmbedModel)
	repo := vectormemory.NewSQLiteRepository(database)
	chatRepo := chat.NewSQLiteRepository(database)

	return backfill(ctx, backfillDeps{
		cfg:       cfg.VectorMemory,
		db:        database,
		distiller: distiller,
		embedder:  embedder,
		repo:      repo,
		chat:      chatRepo,
		logger:    log.New(os.Stdout, "memory-backfill: ", log.LstdFlags),
		now:       time.Now,
	}, dryRun)
}

// session is one undistilled session plus its loaded conversation.
type session struct {
	id       string
	userID   string
	messages []vectormemory.ConversationMessage
}

// batchDistiller / batchEmbedder / memoryRepo / chatStore are the narrow seams
// backfill depends on, so the orchestration is exercisable without real HTTP
// or a real DB.
type batchDistiller interface {
	DistillBatch(ctx context.Context, conversations []string) ([][]string, error)
}

type batchEmbedder interface {
	EmbedBatch(ctx context.Context, inputs []string) ([][]float32, error)
}

type memoryRepo interface {
	Insert(ctx context.Context, m vectormemory.NewMemory) (int64, error)
	NearestDistance(ctx context.Context, userID, model string, query []float32) (float64, bool, error)
}

type chatStore interface {
	SessionMessages(ctx context.Context, sessionID string) ([]chat.Message, error)
	MarkDistilled(ctx context.Context, sessionID string, at time.Time) error
}

type backfillDeps struct {
	cfg       config.VectorMemoryConfig
	db        *sql.DB
	distiller batchDistiller
	embedder  batchEmbedder
	repo      memoryRepo
	chat      chatStore
	logger    *log.Logger
	now       func() time.Time
}

// backfill is the testable orchestration: enumerate undistilled sessions,
// distill them all in one batch, embed all observations in one batch, dedup +
// insert each, then mark every processed session distilled.
func backfill(ctx context.Context, d backfillDeps, dryRun bool) error {
	sessions, err := loadUndistilledSessions(ctx, d)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		d.logger.Printf("no undistilled sessions found; nothing to do")
		return nil
	}

	// Cost-visibility line BEFORE any paid call: one distill request per
	// session goes to the batch API up front.
	d.logger.Printf("STARTING backfill: %d undistilled sessions found; submitting %d distillation requests to the Anthropic batch API (dry_run=%t)",
		len(sessions), len(sessions), dryRun)

	conversations := make([]string, len(sessions))
	for i, s := range sessions {
		conversations[i] = renderConversation(s.messages)
	}

	observationsPerSession, err := d.distiller.DistillBatch(ctx, conversations)
	if err != nil {
		return err
	}
	if len(observationsPerSession) != len(sessions) {
		return errors.New("distill batch returned a mismatched number of results")
	}

	// Flatten observations into one embedding batch, tracking which session
	// each one belongs to so we can attribute the insert.
	var flatObs []string
	var obsSession []int
	for i, obs := range observationsPerSession {
		for _, o := range obs {
			flatObs = append(flatObs, o)
			obsSession = append(obsSession, i)
		}
	}
	d.logger.Printf("distillation produced %d observations across %d sessions; submitting %d embedding requests to the OpenAI batch API",
		len(flatObs), len(sessions), len(flatObs))

	var vecs [][]float32
	if len(flatObs) > 0 {
		vecs, err = d.embedder.EmbedBatch(ctx, flatObs)
		if err != nil {
			return err
		}
		if len(vecs) != len(flatObs) {
			return errors.New("embed batch returned a mismatched number of vectors")
		}
	}

	inserted, err := d.persist(ctx, sessions, flatObs, obsSession, vecs, dryRun)
	if err != nil {
		return err
	}

	marked, err := d.markAll(ctx, sessions, dryRun)
	if err != nil {
		return err
	}

	d.logger.Printf("DONE: sessions=%d observations=%d memories_inserted=%d sessions_marked=%d dry_run=%t",
		len(sessions), len(flatObs), inserted, marked, dryRun)
	return nil
}

// persist dedups (when configured) and inserts each observation, returning the
// count actually inserted. In dry-run it inserts nothing but still reports how
// many it would have inserted (post-dedup is skipped — dedup needs the index).
func (d backfillDeps) persist(ctx context.Context, sessions []session, flatObs []string, obsSession []int, vecs [][]float32, dryRun bool) (int, error) {
	if dryRun {
		// No index writes and no dedup probes in dry-run: report the raw
		// observation count as the would-insert figure.
		return len(flatObs), nil
	}

	dedupEnabled := d.cfg.DedupThreshold > 0
	inserted := 0
	for i, obs := range flatObs {
		vec := vecs[i]
		sess := sessions[obsSession[i]]

		if dedupEnabled {
			dist, found, err := d.repo.NearestDistance(ctx, sess.userID, d.cfg.EmbedModel, vec)
			if err != nil {
				return inserted, err
			}
			if found && dist <= d.cfg.DedupThreshold {
				d.logger.Printf("skipping near-duplicate (session=%s distance=%.4f)", sess.id, dist)
				continue
			}
		}

		if _, err := d.repo.Insert(ctx, vectormemory.NewMemory{
			UserID:          sess.userID,
			DistilledText:   obs,
			SourceSessionID: sess.id,
			EmbeddingModel:  d.cfg.EmbedModel,
			EmbeddingDim:    d.cfg.EmbedDim,
			Embedding:       vec,
			CreatedAt:       d.now().UTC(),
		}); err != nil {
			// One bad row should not throw away a paid distillation: log and
			// continue, matching the live DistillSession policy.
			d.logger.Printf("insert failed, skipping observation (session=%s): %v", sess.id, err)
			continue
		}
		inserted++
	}
	return inserted, nil
}

// markAll stamps every processed session distilled so a re-run skips them.
// Sessions are marked even when they produced zero observations (the
// conversation simply held nothing durable — re-distilling would re-spend for
// the same empty result). Skipped entirely in dry-run.
func (d backfillDeps) markAll(ctx context.Context, sessions []session, dryRun bool) (int, error) {
	if dryRun {
		return 0, nil
	}
	now := d.now()
	marked := 0
	for _, s := range sessions {
		if err := d.chat.MarkDistilled(ctx, s.id, now); err != nil {
			d.logger.Printf("mark distilled failed (session=%s): %v", s.id, err)
			continue
		}
		marked++
	}
	return marked, nil
}

// loadUndistilledSessions enumerates every non-deleted chat session with
// memory_distilled_at IS NULL (the full history, not just idle ones) and loads
// each transcript. A direct SQL query is acceptable here — this is a one-shot
// command, not a reusable repository method.
func loadUndistilledSessions(ctx context.Context, d backfillDeps) ([]session, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, user_id
		FROM chat_sessions
		WHERE memory_distilled_at IS NULL
		  AND deleted_at IS NULL
		ORDER BY last_message_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	type ref struct{ id, userID string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.userID); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]session, 0, len(refs))
	for _, r := range refs {
		msgs, err := d.chat.SessionMessages(ctx, r.id)
		if err != nil {
			return nil, err
		}
		out = append(out, session{
			id:       r.id,
			userID:   r.userID,
			messages: toConversation(msgs),
		})
	}
	return out, nil
}

// toConversation adapts chat.Message to the distiller's ConversationMessage
// shape (vectormemory does not import chat, so the command bridges them).
func toConversation(msgs []chat.Message) []vectormemory.ConversationMessage {
	out := make([]vectormemory.ConversationMessage, len(msgs))
	for i, m := range msgs {
		out[i] = vectormemory.ConversationMessage{
			Role:    string(m.Role),
			Content: m.Content,
		}
	}
	return out
}

// renderConversation flattens turns into the "role: content" transcript the
// distiller reads — mirrors the service's private renderConversation so the
// batch and live paths see identical input formatting.
func renderConversation(messages []vectormemory.ConversationMessage) string {
	var b []byte
	for _, m := range messages {
		b = append(b, m.Role...)
		b = append(b, ':', ' ')
		b = append(b, m.Content...)
		b = append(b, '\n')
	}
	return string(b)
}
