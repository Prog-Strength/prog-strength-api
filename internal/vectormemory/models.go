package vectormemory

import "time"

// Memory is one distilled, durable observation about a user, plus its
// provenance and the embedding-model metadata that guards against
// comparing vectors from different models.
type Memory struct {
	ID              int64
	UserID          string
	DistilledText   string
	SourceType      string  // "chat_session" | "workout_note" | "activity_note"
	SourceSessionID *string // set iff SourceType == "chat_session"
	SourceMessageID *int64  // best-effort, chat only
	// SourceWorkoutID is the activities row a note came from — a lift for
	// "workout_note", an endurance session for "activity_note". The column
	// name predates unification (migration 042 re-pointed it at activities);
	// the sport is recovered by joining activities.activity_type.
	SourceWorkoutID *string
	EmbeddingModel  string
	EmbeddingDim    int
	SupersededAt    *time.Time
	CreatedAt       time.Time
}

// Match is one retrieval hit: the stored text plus the cosine distance to
// the query and the provenance the probe surfaces. The agent reads only Text
// (see the agent's api_client.retrieve_memories, which projects it out); the
// admin search path returns the rest.
//
// SourceType and the two FKs mirror Memory's, so a probe hit can be traced
// back to what produced it exactly as a dump row can. Exactly one FK is
// populated per hit — the schema CHECK ties which one to SourceType — and the
// unused one is the empty string, never the other's value.
type Match struct {
	Text            string    `json:"text"`
	Distance        float64   `json:"distance"`
	SourceType      string    `json:"source_type"`
	SourceSessionID string    `json:"source_session_id"`
	SourceWorkoutID string    `json:"source_workout_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// NewMemory is the insert input: the text row fields plus the vector. The
// repo writes the text row and the vector row in one transaction. SourceType
// selects which typed FK is populated (the other is written NULL).
type NewMemory struct {
	UserID          string
	DistilledText   string
	SourceType      string
	SourceSessionID *string
	SourceMessageID *int64
	SourceWorkoutID *string
	EmbeddingModel  string
	EmbeddingDim    int
	Embedding       []float32
	CreatedAt       time.Time
}
