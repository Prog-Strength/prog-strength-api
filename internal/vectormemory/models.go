package vectormemory

import "time"

// Memory is one distilled, durable observation about a user, plus its
// provenance and the embedding-model metadata that guards against
// comparing vectors from different models.
type Memory struct {
	ID              int64
	UserID          string
	DistilledText   string
	SourceSessionID string
	SourceMessageID *int64
	EmbeddingModel  string
	EmbeddingDim    int
	SupersededAt    *time.Time
	CreatedAt       time.Time
}

// Match is one retrieval hit: the stored text plus the cosine distance to
// the query and the provenance the probe surfaces. The agent ignores
// Distance; the admin search path returns it.
type Match struct {
	Text            string    `json:"text"`
	Distance        float64   `json:"distance"`
	SourceSessionID string    `json:"source_session_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// NewMemory is the insert input: the text row fields plus the vector. The
// repo writes the text row and the vector row in one transaction.
type NewMemory struct {
	UserID          string
	DistilledText   string
	SourceSessionID string
	SourceMessageID *int64
	EmbeddingModel  string
	EmbeddingDim    int
	Embedding       []float32
	CreatedAt       time.Time
}
