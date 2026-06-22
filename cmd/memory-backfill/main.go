// Command memory-backfill is a ONE-TIME, MANUAL-RUN tool that seeds the agent
// vector-memory index from the full existing corpus of EVERY registered memory
// source — historical chat conversations AND the existing workout-note log — in
// a single run. It is NOT invoked automatically anywhere (no cron, no server
// hook, no background job). Run it by hand exactly once (or re-run safely: it is
// idempotent — already-distilled units are skipped because each source's
// AllUndistilled filters on its own memory_distilled_at IS NULL marker).
//
// Source-aware: the backfill ranges over the SAME source registry the live
// distillation job uses (server.BuildMemorySources) and, per source, drains
// AllUndistilled in keyset-paginated pages, distilling each page with that
// source's PromptHint and writing each observation with the unit's typed
// provenance (chat → source_session_id, workout → source_workout_id).
//
// why batch APIs: this is a single, large, latency-insensitive job, so it uses
// the half-price async Batch APIs (vectormemory.BatchDistiller +
// vectormemory.BatchEmbedder) — each page's conversations go to one Anthropic
// Message Batch, the page's resulting observations to one OpenAI embeddings
// Batch. Both calls BLOCK until their batch completes (minutes-to-hours), which
// is the intended behavior for a run-once seeding tool.
//
// Cost visibility: counts are logged liberally throughout, per source and in
// total (units found, observations produced, memories inserted, units marked).
//
// --dry-run distills + embeds and prints the counts WITHOUT writing any memory
// rows or marking any unit distilled. It still spends on the batch APIs (it
// has to, to produce the counts), so it is a "preview the result, change
// nothing" switch rather than a "spend nothing" one.
package main

import (
	"context"
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
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/server"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// backfillHTTPTimeout bounds each individual HTTP call (upload, create, one
// poll, one download) — NOT the whole batch, which spans many polls. Batch
// jobs run for a long wall-clock time, but every single request is quick.
const backfillHTTPTimeout = 60 * time.Second

// backfillPageSize bounds how many undistilled units one AllUndistilled call
// returns, so each source is drained in keyset-paginated pages rather than one
// unbounded slice. The existing workout-note corpus may be large; paging keeps
// the command's working set bounded and makes the run resumable — every page's
// units are marked distilled before the next page is fetched, so an interrupted
// run picks up where it left off on re-run.
const backfillPageSize = 200

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

	// Range over the SAME registry the live distillation job uses, so one run
	// seeds every source's existing corpus (chat history + workout notes). The
	// command may import internal/server: server does not import cmd, so there is
	// no import cycle.
	sources := server.BuildMemorySources(database, chatRepo, cfg.VectorMemory)

	return backfill(ctx, backfillDeps{
		cfg:       cfg.VectorMemory,
		distiller: distiller,
		embedder:  embedder,
		repo:      repo,
		sources:   sources,
		logger:    log.New(os.Stdout, "memory-backfill: ", log.LstdFlags),
		now:       time.Now,
	}, dryRun)
}

// batchDistiller / batchEmbedder / memoryRepo are the narrow seams backfill
// depends on, so the orchestration is exercisable without real HTTP. Sources are
// injected as vectormemory.MemorySource so the page drain is exercisable with
// fakes (no real registry or live providers).
type batchDistiller interface {
	DistillBatch(ctx context.Context, conversations []string, promptHint string) ([][]string, error)
}

type batchEmbedder interface {
	EmbedBatch(ctx context.Context, inputs []string) ([][]float32, error)
}

type memoryRepo interface {
	Insert(ctx context.Context, m vectormemory.NewMemory) (int64, error)
	NearestDistance(ctx context.Context, userID, model string, query []float32) (float64, bool, error)
}

type backfillDeps struct {
	cfg       config.VectorMemoryConfig
	distiller batchDistiller
	embedder  batchEmbedder
	repo      memoryRepo
	sources   []vectormemory.MemorySource
	logger    *log.Logger
	now       func() time.Time
}

// backfill is the testable orchestration: range over every source and, per
// source, drain AllUndistilled in keyset-paginated pages. Each page is distilled
// in one batch (with the source's PromptHint), its observations embedded in one
// batch, then deduped + inserted with each unit's typed provenance, and every
// processed unit marked distilled before the next page is fetched.
func backfill(ctx context.Context, d backfillDeps, dryRun bool) error {
	var totalUnits, totalObs, totalInserted, totalMarked int

	for _, src := range d.sources {
		srcType := src.SourceType()
		var srcUnits, srcObs, srcInserted, srcMarked int

		cursor := ""
		for {
			units, next, err := src.AllUndistilled(ctx, cursor, backfillPageSize)
			if err != nil {
				return err
			}
			if len(units) == 0 {
				break
			}
			srcUnits += len(units)

			// Cost-visibility line BEFORE the paid call: one distill request per
			// unit in this page. Every unit in a single source shares one prompt
			// hint, so units[0].PromptHint is representative for the whole page.
			d.logger.Printf("source=%s: %d undistilled units in page; submitting %d distillation requests to the Anthropic batch API (dry_run=%t)",
				srcType, len(units), len(units), dryRun)

			contents := make([]string, len(units))
			for i, u := range units {
				contents[i] = u.Content
			}

			observationsPerUnit, err := d.distiller.DistillBatch(ctx, contents, units[0].PromptHint)
			if err != nil {
				return err
			}
			if len(observationsPerUnit) != len(units) {
				return errors.New("distill batch returned a mismatched number of results")
			}

			// Flatten observations into one embedding batch, tracking which unit
			// each one belongs to so the insert carries the right provenance.
			var flatObs []string
			var obsUnit []int
			for i, obs := range observationsPerUnit {
				for _, o := range obs {
					flatObs = append(flatObs, o)
					obsUnit = append(obsUnit, i)
				}
			}
			srcObs += len(flatObs)
			d.logger.Printf("source=%s: distillation produced %d observations across %d units; submitting %d embedding requests to the OpenAI batch API",
				srcType, len(flatObs), len(units), len(flatObs))

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

			inserted, err := d.persist(ctx, units, flatObs, obsUnit, vecs, dryRun)
			if err != nil {
				return err
			}
			srcInserted += inserted

			marked, err := d.markAll(ctx, src, units, dryRun)
			if err != nil {
				return err
			}
			srcMarked += marked

			if next == "" {
				break
			}
			cursor = next
		}

		d.logger.Printf("source=%s DONE: units=%d observations=%d memories_inserted=%d units_marked=%d dry_run=%t",
			srcType, srcUnits, srcObs, srcInserted, srcMarked, dryRun)
		totalUnits += srcUnits
		totalObs += srcObs
		totalInserted += srcInserted
		totalMarked += srcMarked
	}

	d.logger.Printf("ALL SOURCES DONE: units=%d observations=%d memories_inserted=%d units_marked=%d dry_run=%t",
		totalUnits, totalObs, totalInserted, totalMarked, dryRun)
	return nil
}

// persist dedups (when configured) and inserts each observation, returning the
// count actually inserted. In dry-run it inserts nothing but still reports how
// many it would have inserted (post-dedup is skipped — dedup needs the index).
func (d backfillDeps) persist(ctx context.Context, units []vectormemory.DistillUnit, flatObs []string, obsUnit []int, vecs [][]float32, dryRun bool) (int, error) {
	if dryRun {
		// No index writes and no dedup probes in dry-run: report the raw
		// observation count as the would-insert figure.
		return len(flatObs), nil
	}

	dedupEnabled := d.cfg.DedupThreshold > 0
	inserted := 0
	for i, obs := range flatObs {
		vec := vecs[i]
		unit := units[obsUnit[i]]

		if dedupEnabled {
			dist, found, err := d.repo.NearestDistance(ctx, unit.UserID, d.cfg.EmbedModel, vec)
			if err != nil {
				return inserted, err
			}
			if found && dist <= d.cfg.DedupThreshold {
				d.logger.Printf("skipping near-duplicate (unit=%s distance=%.4f)", unit.UnitID, dist)
				continue
			}
		}

		if _, err := d.repo.Insert(ctx, vectormemory.NewMemory{
			UserID:          unit.UserID,
			DistilledText:   obs,
			SourceType:      unit.Source.SourceType,
			SourceSessionID: unit.Source.SessionID,
			SourceMessageID: unit.Source.MessageID,
			SourceWorkoutID: unit.Source.WorkoutID,
			EmbeddingModel:  d.cfg.EmbedModel,
			EmbeddingDim:    d.cfg.EmbedDim,
			Embedding:       vec,
			CreatedAt:       d.now().UTC(),
		}); err != nil {
			// One bad row should not throw away a paid distillation: log and
			// continue, matching the live DistillUnit policy.
			d.logger.Printf("insert failed, skipping observation (unit=%s): %v", unit.UnitID, err)
			continue
		}
		inserted++
	}
	return inserted, nil
}

// markAll stamps every processed unit distilled so a re-run skips it. Units are
// marked even when they produced zero observations (the content simply held
// nothing durable — re-distilling would re-spend for the same empty result).
// Skipped entirely in dry-run.
func (d backfillDeps) markAll(ctx context.Context, src vectormemory.MemorySource, units []vectormemory.DistillUnit, dryRun bool) (int, error) {
	if dryRun {
		return 0, nil
	}
	now := d.now()
	marked := 0
	for _, u := range units {
		if err := src.MarkDistilled(ctx, u.UnitID, now); err != nil {
			d.logger.Printf("mark distilled failed (unit=%s): %v", u.UnitID, err)
			continue
		}
		marked++
	}
	return marked, nil
}
