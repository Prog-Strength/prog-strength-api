package telemetry

import "context"

// Repository persists agent telemetry. Implementations are SQLite
// today; the interface stays narrow so a future move to a columnar
// store (DuckDB, etc.) wouldn't require changes at call sites.
//
// All write methods accept already-populated structs — the handler
// validates and assigns IDs/timestamps before calling. No defaulting
// inside the repo, so backfill paths can pass deterministic IDs.
type Repository interface {
	// InsertTurn persists a single agent_turns row. Returns
	// ErrConflict if the turn ID is already present (retries from the
	// agent are idempotent by ID).
	InsertTurn(ctx context.Context, t AgentTurn) error

	// InsertToolCalls persists zero or more agent_tool_calls rows.
	// All inserts happen inside a single transaction so a partial
	// batch never lands.
	InsertToolCalls(ctx context.Context, calls []AgentToolCall) error

	// InsertMessages persists zero or more agent_messages rows.
	// Same all-or-nothing batch semantics as InsertToolCalls.
	InsertMessages(ctx context.Context, msgs []AgentMessage) error
}
