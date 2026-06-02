// Package telemetry persists agent-side runtime observability data:
// per-chat-turn metadata, MCP tool-call traces, and the user/assistant
// message log. Writes are received from the FastAPI agent service via
// the API's internal-only HTTP endpoints (`/internal/telemetry/*`)
// and persisted into a dedicated SQLite database (`telemetry.db`)
// that is separate from the application database (`app.db`).
//
// The "Go API is the sole SQLite writer" rule still holds — the
// telemetry package opens its own *sql.DB handle, but the agent and
// MCP server never touch SQLite directly.
//
// See prog-strength-docs/sows/monitoring-and-observability.md for
// the full design.
package telemetry

import "time"

// AgentTurn captures one /chat request handled by the agent service.
// All fields are required at write time; the API rejects partial
// rows with a 400 so analyses never see half-populated turns.
type AgentTurn struct {
	ID                  string
	UserID              string
	SessionID           string
	Model               string
	RoutedTier          string
	RouterModel         string
	RouterLatencyMs     int
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	TotalLatencyMs      int
	TimeToFirstTokenMs  int
	CompletionReason    string
	// Error is populated only when CompletionReason == "error".
	Error *string
	// Intent classification produced by the agent's router. One of
	// log_nutrition | log_workout | log_bodyweight | analyze_progress |
	// general. Empty string means the router didn't run.
	Intent string
	// Wall-clock time spent running the intent's prefetch tool calls
	// (asyncio.gather across multiple MCP tools). Zero when no
	// prefetch ran (intent == general or router failed).
	IntentPrefetchDurationMs int
	// True when any prefetch tool call raised. The harness still
	// composes the prompt and runs the turn — degraded enrichment is
	// silent — but this flag lets the dashboard surface the rate.
	IntentPrefetchFailed bool
	StartedAt            time.Time
	EndedAt    time.Time
	CreatedAt  time.Time
}

// AgentToolCall captures a single MCP tool invocation during a turn.
// ArgumentsJSON and ResultSummary are nullable because the daily TTL
// job NULLs them out after 90 days — metadata (ToolName, LatencyMs,
// Error) stays forever.
type AgentToolCall struct {
	ID            string
	TurnID        string
	ToolName      string
	ArgumentsJSON *string
	ResultSummary *string
	LatencyMs     int
	Error         *string
	StartedAt     time.Time
	EndedAt       time.Time
	CreatedAt     time.Time
}

// AgentMessage captures one user or assistant message. Content is
// nullable because the TTL job NULLs it after 90 days; TokenCount and
// other metadata stay.
type AgentMessage struct {
	ID         string
	TurnID     string
	Role       string  // "user" | "assistant"
	Content    *string
	TokenCount *int
	CreatedAt  time.Time
}
