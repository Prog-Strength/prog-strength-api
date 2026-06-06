package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Compile-time check.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository writes telemetry to the dedicated telemetry.db
// SQLite file. The *sql.DB handle is owned by this struct; the
// server wires it up at startup alongside the app.db handle.
type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (r *SQLiteRepository) InsertTurn(ctx context.Context, t AgentTurn) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO agent_turns (
			id, user_id, session_id,
			model, routed_tier, router_model, router_latency_ms,
			input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			total_latency_ms, time_to_first_token_ms,
			completion_reason, error,
			intent, intent_prefetch_duration_ms, intent_prefetch_failed,
			had_image,
			started_at, ended_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		t.ID, t.UserID, t.SessionID,
		t.Model, t.RoutedTier, t.RouterModel, t.RouterLatencyMs,
		t.InputTokens, t.OutputTokens, t.CacheCreationTokens, t.CacheReadTokens,
		t.TotalLatencyMs, t.TimeToFirstTokenMs,
		t.CompletionReason, t.Error,
		t.Intent, t.IntentPrefetchDurationMs, boolToInt(t.IntentPrefetchFailed),
		boolToInt(t.HadImage),
		t.StartedAt, t.EndedAt, t.CreatedAt,
	)
	if err != nil {
		// SQLite returns UNIQUE constraint violations as a generic
		// "constraint failed" error. Translate to a typed error so the
		// handler can map to 409 without string-matching.
		if isUniqueConstraintErr(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (r *SQLiteRepository) InsertToolCalls(ctx context.Context, calls []AgentToolCall) error {
	if len(calls) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, c := range calls {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_tool_calls (
				id, turn_id, tool_name,
				arguments_json, result_summary,
				latency_ms, error,
				started_at, ended_at, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			c.ID, c.TurnID, c.ToolName,
			c.ArgumentsJSON, c.ResultSummary,
			c.LatencyMs, c.Error,
			c.StartedAt, c.EndedAt, c.CreatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLiteRepository) InsertMessages(ctx context.Context, msgs []AgentMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, m := range msgs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_messages (
				id, turn_id, role, content, token_count, created_at
			) VALUES (?, ?, ?, ?, ?, ?)
		`,
			m.ID, m.TurnID, m.Role, m.Content, m.TokenCount, m.CreatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// isUniqueConstraintErr detects mattn/go-sqlite3's "UNIQUE constraint
// failed" error string. Not exhaustive — the driver also exports a
// typed Error with ExtendedCode == ErrConstraintUnique — but the
// string check is dependency-free and matches what the existing
// repositories in this codebase don't yet need.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// boolToInt converts a bool to the SQLite INTEGER convention (1/0).
// SQLite has no native boolean type; storing as INTEGER is idiomatic
// and matches the column definitions in telemetry_migrations.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
