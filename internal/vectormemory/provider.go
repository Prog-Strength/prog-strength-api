package vectormemory

import "context"

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

// Distiller reads a conversation and returns zero or more atomic durable
// observations worth remembering long-term, plus the call's token usage for
// cost metrics. Output is forced to a JSON array via a tool-call schema so it
// is always a (possibly empty) list.
type Distiller interface {
	Distill(ctx context.Context, conversation string) ([]string, DistillUsage, error)
	Configured() bool
}
