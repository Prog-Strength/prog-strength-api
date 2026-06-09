package telemetry

import (
	"context"
	"log"
	"time"
)

// ContentRetention is how long agent message content and tool-call
// inputs/outputs are kept in telemetry.db before the daily TTL job
// NULLs them out. Metadata (token counts, latencies, timestamps,
// model names, tool names) is never purged — only the bulky content
// columns age out. Per the SOW.
const ContentRetention = 90 * 24 * time.Hour

// ttlTickInterval is how often runTTL wakes up. Daily matches the
// SOW; a tighter interval doesn't change the retention semantics but
// adds noise to logs.
const ttlTickInterval = 24 * time.Hour

// StartContentTTL launches a background goroutine that periodically
// NULLs out aged message and tool-call content. Returns immediately;
// the goroutine runs until ctx is canceled.
//
// Called once from server.New(); ctx is context.Background() today
// since the existing startup paths (backfills, migrations) use the
// same. The OS reaps the goroutine on process exit; a graceful-
// shutdown context can be plumbed through later if it becomes
// useful for shutdown logs.
func (r *SQLiteRepository) StartContentTTL(ctx context.Context, retention time.Duration) {
	go r.runTTL(ctx, retention)
}

func (r *SQLiteRepository) runTTL(ctx context.Context, retention time.Duration) {
	// Run once at start so a freshly-restored telemetry.db immediately
	// becomes consistent with the retention policy rather than waiting
	// up to 24h. Cheap on an empty DB; bounded by row count on a full
	// one (UPDATE … WHERE created_at < ? is one index scan).
	if err := r.purgeAgedContent(ctx, retention); err != nil {
		log.Printf("telemetry ttl: initial purge failed: %v", err)
	}

	ticker := time.NewTicker(ttlTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.purgeAgedContent(ctx, retention); err != nil {
				log.Printf("telemetry ttl: purge failed: %v", err)
			}
		}
	}
}

// purgeAgedContent NULLs out content older than `retention` in both
// content-bearing tables. Two separate UPDATEs (rather than a single
// joined statement) because the columns and tables don't share an
// index and the two queries don't benefit from being inside one
// transaction — neither is a partial-failure concern.
func (r *SQLiteRepository) purgeAgedContent(ctx context.Context, retention time.Duration) error {
	cutoff := time.Now().Add(-retention)

	// agent_messages.content stays NULL'd; the row keeps its
	// metadata (turn_id, role, token_count, created_at). Future
	// analyses can still see "the user said something here" without
	// knowing what.
	msgRes, err := r.db.ExecContext(ctx, `
		UPDATE agent_messages
		SET content = NULL
		WHERE content IS NOT NULL AND created_at < ?
	`, cutoff)
	if err != nil {
		return err
	}
	msgs, _ := msgRes.RowsAffected()

	// agent_tool_calls.arguments_json and result_summary both
	// age out together — they're symmetric "what went in / what
	// came out" inputs that only make sense as a pair.
	toolRes, err := r.db.ExecContext(ctx, `
		UPDATE agent_tool_calls
		SET arguments_json = NULL, result_summary = NULL
		WHERE arguments_json IS NOT NULL AND created_at < ?
	`, cutoff)
	if err != nil {
		return err
	}
	tools, _ := toolRes.RowsAffected()

	if msgs > 0 || tools > 0 {
		log.Printf("telemetry ttl: nulled %d messages, %d tool calls older than %s",
			msgs, tools, retention)
	}
	return nil
}
