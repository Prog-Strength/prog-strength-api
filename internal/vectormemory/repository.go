package vectormemory

import "context"

// Repository persists agent vector memory: the distilled-text rows in
// agent_memories and their float vectors in the sqlite-vec vec0 virtual
// table vec_agent_memories. The two are kept in lockstep — every write
// goes through Insert in one transaction, and reads JOIN the vec table
// back to the text table so an orphaned vec row never surfaces.
type Repository interface {
	// Insert writes the text row and the vector row in one transaction
	// and returns the new agent_memories id.
	// why: the vec0 row's memory_id is the agent_memories id, so the text
	// row must be inserted first to mint the id; doing both in one tx keeps
	// the two tables from drifting if the second insert fails.
	Insert(ctx context.Context, m NewMemory) (int64, error)

	// Search returns up to k of userID's non-superseded memories for the
	// active model, within maxDistance cosine distance, ordered ascending
	// by distance. maxDistance <= 0 means no distance cap (still capped by k).
	// why: retrieval is per-user and per-model — vectors from a different
	// embedding model aren't comparable — and the distance cap is the
	// relevance gate so the agent never recalls a barely-related memory.
	Search(ctx context.Context, userID, model string, query []float32, k int, maxDistance float64) ([]Match, error)

	// NearestDistance returns the cosine distance to the single closest
	// non-superseded memory for the model, or (0, false, nil) when the user
	// has none for that model.
	// why: the distillation dedup probe asks "is this observation close
	// enough to one we already have?" — it needs the nearest distance, not
	// the text — so this is a cheaper k=1 path than Search.
	NearestDistance(ctx context.Context, userID, model string, query []float32) (float64, bool, error)

	// Dump returns a flat page of memories newest-first, optionally filtered
	// to one user (empty userID = all users), INCLUDING superseded rows.
	// why: the admin dump inspects the raw store for debugging, so it must
	// show superseded rows the retrieval path hides; it reads agent_memories
	// directly and never touches the vec table.
	Dump(ctx context.Context, userID string, limit, offset int) ([]Memory, error)
}
