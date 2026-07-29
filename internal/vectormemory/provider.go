package vectormemory

import (
	"context"
	"errors"
	"net/http"
)

// ErrUnitUnprocessable tags a distillation failure caused by the unit itself:
// the provider rejected the request, and re-sending identical content will be
// rejected identically.
//
// why: the distillation job stamps a unit distilled only on success, so a
// deterministically-failing unit is re-selected and re-rejected on every tick,
// forever — a permanent error rate on the dashboard and a queue that never
// drains (issue #78). Providers wrap their terminal rejections with this so the
// job can drop the unit instead of looping on it. It is deliberately narrow:
// anything that might succeed on a later attempt (rate limits, timeouts, 5xx,
// auth) must NOT carry it, or a transient outage would silently discard real
// conversations.
var ErrUnitUnprocessable = errors.New("vectormemory: unit unprocessable")

// terminalStatus reports whether a provider HTTP status means "this exact
// request will never succeed".
//
// Only 4xx qualifies, and not all of it:
//   - 408 / 429 are explicitly retryable — the request was fine, the timing
//     wasn't.
//   - 401 / 403 are a misconfigured key, not a bad unit. Dropping units on an
//     expired credential would quietly burn the whole backlog, so they stay
//     retryable and keep failing loudly until the key is fixed.
func terminalStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusUnauthorized, http.StatusForbidden:
		return false
	}
	return code >= 400 && code < 500
}

// EmbedUsage is the token spend reported by an embedding call. TotalTokens
// comes from the provider's usage block; it is 0 when the response omits one
// (a missing usage block is not an error — it just means no cost signal).
type EmbedUsage struct {
	TotalTokens int
}

// DistillUsage is the token spend reported by a distill call, split into the
// input (prompt) and output (completion) sides the provider bills separately.
// Both are 0 when the response omits a usage block.
type DistillUsage struct {
	InputTokens  int
	OutputTokens int
}

// Embedder turns text into vectors via an embedding model. One live
// implementation (OpenAI); the backfill command uses a Batch variant.
type Embedder interface {
	// Embed returns one vector per input string, in order, plus the call's
	// token usage for cost metrics. Empty input returns an empty slice and a
	// zero-value usage without calling out.
	Embed(ctx context.Context, inputs []string) ([][]float32, EmbedUsage, error)
	Configured() bool
}

// Distiller reads a unit's assembled content and returns zero or more atomic
// durable observations worth remembering long-term, plus the call's token usage
// for cost metrics. Output is forced to a JSON array via a tool-call schema so
// it is always a (possibly empty) list. promptHint is a per-source framing
// string appended to the system prompt; it is "" for chat (behavior
// unchanged) and source-specific for other sources.
type Distiller interface {
	Distill(ctx context.Context, content, promptHint string) ([]string, DistillUsage, error)
	Configured() bool
}
