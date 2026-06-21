package vectormemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
)

// OpenAI embeddings provider.
//
// why: embeddings are the retrieval substrate — every memory and every
// query is turned into a vector here, and cosine distance between those
// vectors is what surfaces a relevant memory. The offline backfill
// command uses a Batch variant of this same model so historical and
// live memories stay in the same vector space.

const openAIEmbeddingsURL = "https://api.openai.com/v1/embeddings"

// errBodyLimit caps how much of a non-200 response body we read back for
// error context — enough to be useful, not enough to log a megabyte.
const errBodyLimit = 1 << 11 // 2 KiB

// Compile-time check that *OpenAIEmbedder satisfies Embedder.
var _ Embedder = (*OpenAIEmbedder)(nil)

type OpenAIEmbedder struct {
	client *http.Client
	apiKey string
	model  string

	// BaseURL defaults to the production embeddings endpoint; tests
	// point it at an httptest server.
	BaseURL string
}

func NewOpenAIEmbedder(client *http.Client, apiKey, model string) *OpenAIEmbedder {
	return &OpenAIEmbedder{client: client, apiKey: apiKey, model: model, BaseURL: openAIEmbeddingsURL}
}

func (e *OpenAIEmbedder) Configured() bool { return e.apiKey != "" }

func (e *OpenAIEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	reqBody, err := json.Marshal(map[string]any{
		"model": e.model,
		"input": inputs,
	})
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return nil, fmt.Errorf("openai embeddings: unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}

	var payload struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("openai embeddings: decode response: %w", err)
	}
	if len(payload.Data) != len(inputs) {
		return nil, fmt.Errorf("openai embeddings: got %d vectors for %d inputs", len(payload.Data), len(inputs))
	}

	// OpenAI returns data in input order, but the index field is the
	// contract — sort by it defensively so a vector never lands against
	// the wrong input.
	sort.Slice(payload.Data, func(i, j int) bool {
		return payload.Data[i].Index < payload.Data[j].Index
	})

	out := make([][]float32, len(payload.Data))
	for i, d := range payload.Data {
		vec := make([]float32, len(d.Embedding))
		for j, v := range d.Embedding {
			vec[j] = float32(v)
		}
		out[i] = vec
	}
	return out, nil
}
