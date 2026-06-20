package vectormemory

import "context"

// Embedder turns text into vectors via an embedding model. One live
// implementation (OpenAI); the backfill command uses a Batch variant.
type Embedder interface {
	// Embed returns one vector per input string, in order. Empty input
	// returns an empty slice without calling out.
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	Configured() bool
}

// Distiller reads a conversation and returns zero or more atomic durable
// observations worth remembering long-term. Output is forced to a JSON
// array via a tool-call schema so it is always a (possibly empty) list.
type Distiller interface {
	Distill(ctx context.Context, conversation string) ([]string, error)
	Configured() bool
}
