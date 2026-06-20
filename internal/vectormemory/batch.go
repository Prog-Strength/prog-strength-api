package vectormemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Batch provider variants for the one-time offline backfill.
//
// why: seeding the vector index from the full chat history is a single,
// large, latency-insensitive job. The half-price async Batch APIs are the
// right tool for that — we submit every conversation/observation in one
// request, then poll for completion. These live alongside the synchronous
// providers (openai.go, anthropic.go) and reuse the same raw net/http style
// (injected *http.Client, overridable BaseURL for tests, no SDK). They share
// the distill request-body builder and tool_use parser with anthropic.go so
// the tool schema + system prompt are defined exactly once.

const (
	openAIBatchBaseURL    = "https://api.openai.com/v1"
	anthropicBatchBaseURL = "https://api.anthropic.com/v1"

	// defaultBatchPollInterval is how long EmbedBatch/DistillBatch sleep
	// between status polls. Batch jobs run for minutes-to-hours, so a few
	// seconds between polls is plenty; tests override it to ~1ms.
	defaultBatchPollInterval = 5 * time.Second

	// anthropicBatchBeta is the beta header gating the Message Batches API.
	anthropicBatchBeta = "message-batches-2024-09-24"
)

// BatchEmbedder embeds a slice of inputs via the OpenAI Batch API. EmbedBatch
// blocks until the batch completes (uploading a JSONL file, creating a batch,
// polling, then downloading results), so the caller treats it like a slow
// synchronous Embed.
type BatchEmbedder struct {
	client *http.Client
	apiKey string
	model  string

	// BaseURL is the API root (default https://api.openai.com/v1); tests
	// point it at an httptest server. Paths (/files, /batches, ...) are
	// appended to it.
	BaseURL string

	// PollInterval is the sleep between batch-status polls (default 5s).
	PollInterval time.Duration
}

func NewBatchEmbedder(client *http.Client, apiKey, model string) *BatchEmbedder {
	return &BatchEmbedder{
		client:       client,
		apiKey:       apiKey,
		model:        model,
		BaseURL:      openAIBatchBaseURL,
		PollInterval: defaultBatchPollInterval,
	}
}

func (e *BatchEmbedder) Configured() bool { return e.apiKey != "" }

// EmbedBatch embeds every input via the OpenAI Batch API and returns the
// vectors in input order, blocking until the batch completes.
func (e *BatchEmbedder) EmbedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	jsonl, err := e.buildInputJSONL(inputs)
	if err != nil {
		return nil, err
	}

	fileID, err := e.uploadFile(ctx, jsonl)
	if err != nil {
		return nil, err
	}

	batchID, err := e.createBatch(ctx, fileID)
	if err != nil {
		return nil, err
	}

	outputFileID, err := e.pollBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}

	content, err := e.fileContent(ctx, outputFileID)
	if err != nil {
		return nil, err
	}

	return e.parseResults(content, inputs)
}

// buildInputJSONL renders one embeddings request per input, custom_id "emb-<i>".
func (e *BatchEmbedder) buildInputJSONL(inputs []string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i, in := range inputs {
		line := map[string]any{
			"custom_id": "emb-" + strconv.Itoa(i),
			"method":    http.MethodPost,
			"url":       "/v1/embeddings",
			"body": map[string]any{
				"model": e.model,
				"input": in,
			},
		}
		if err := enc.Encode(line); err != nil {
			return nil, fmt.Errorf("openai batch: encode jsonl line %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}

// uploadFile POSTs the JSONL as multipart/form-data with purpose=batch and
// returns the resulting file id.
func (e *BatchEmbedder) uploadFile(ctx context.Context, jsonl []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("purpose", "batch"); err != nil {
		return "", fmt.Errorf("openai batch: write purpose field: %w", err)
	}
	part, err := mw.CreateFormFile("file", "batch.jsonl")
	if err != nil {
		return "", fmt.Errorf("openai batch: create file part: %w", err)
	}
	if _, werr := part.Write(jsonl); werr != nil {
		return "", fmt.Errorf("openai batch: write file part: %w", werr)
	}
	if cerr := mw.Close(); cerr != nil {
		return "", fmt.Errorf("openai batch: close multipart writer: %w", cerr)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/files", &body)
	if err != nil {
		return "", fmt.Errorf("openai batch: build upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	var out struct {
		ID string `json:"id"`
	}
	if err := e.doJSON(req, &out); err != nil {
		return "", fmt.Errorf("openai batch: upload file: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("openai batch: upload file: empty file id in response")
	}
	return out.ID, nil
}

// createBatch creates the embeddings batch from an uploaded file and returns
// its id.
func (e *BatchEmbedder) createBatch(ctx context.Context, fileID string) (string, error) {
	reqBody, err := json.Marshal(map[string]any{
		"input_file_id":     fileID,
		"endpoint":          "/v1/embeddings",
		"completion_window": "24h",
	})
	if err != nil {
		return "", fmt.Errorf("openai batch: marshal create request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/batches", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("openai batch: build create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := e.doJSON(req, &out); err != nil {
		return "", fmt.Errorf("openai batch: create batch: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("openai batch: create batch: empty batch id in response")
	}
	return out.ID, nil
}

// pollBatch polls the batch until it completes, returning the output file id.
// It returns an error on failed/expired/canceled terminal states.
func (e *BatchEmbedder) pollBatch(ctx context.Context, batchID string) (string, error) {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.BaseURL+"/batches/"+batchID, nil)
		if err != nil {
			return "", fmt.Errorf("openai batch: build poll request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+e.apiKey)

		var out struct {
			Status       string `json:"status"`
			OutputFileID string `json:"output_file_id"`
		}
		if err := e.doJSON(req, &out); err != nil {
			return "", fmt.Errorf("openai batch: poll batch: %w", err)
		}

		switch out.Status {
		case "completed":
			if out.OutputFileID == "" {
				return "", fmt.Errorf("openai batch: completed batch has no output_file_id")
			}
			return out.OutputFileID, nil
		case "failed", "expired", "cancelled", "cancelling": //nolint:misspell // OpenAI's literal batch status values use British spelling
			return "", fmt.Errorf("openai batch: terminal status %q", out.Status)
		}

		if err := sleepCtx(ctx, e.PollInterval); err != nil {
			return "", err
		}
	}
}

// fileContent downloads the JSONL body of an output file.
func (e *BatchEmbedder) fileContent(ctx context.Context, fileID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.BaseURL+"/files/"+fileID+"/content", nil)
	if err != nil {
		return nil, fmt.Errorf("openai batch: build content request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai batch: fetch content: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return nil, fmt.Errorf("openai batch: fetch content: unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai batch: read content: %w", err)
	}
	return body, nil
}

// parseResults maps each output line's custom_id back to its input index and
// reassembles the embeddings in input order (float64 → float32).
func (e *BatchEmbedder) parseResults(content []byte, inputs []string) ([][]float32, error) {
	byIndex := make(map[int][]float32, len(inputs))
	dec := json.NewDecoder(bytes.NewReader(content))
	for dec.More() {
		var line struct {
			CustomID string `json:"custom_id"`
			Response struct {
				StatusCode int `json:"status_code"`
				Body       struct {
					Data []struct {
						Embedding []float64 `json:"embedding"`
						Index     int       `json:"index"`
					} `json:"data"`
				} `json:"body"`
			} `json:"response"`
		}
		if err := dec.Decode(&line); err != nil {
			return nil, fmt.Errorf("openai batch: decode result line: %w", err)
		}
		idx, err := customIndex(line.CustomID, "emb-")
		if err != nil {
			return nil, fmt.Errorf("openai batch: %w", err)
		}
		if line.Response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("openai batch: input %d returned status %d", idx, line.Response.StatusCode)
		}
		if len(line.Response.Body.Data) == 0 {
			return nil, fmt.Errorf("openai batch: input %d returned no embedding", idx)
		}
		raw := line.Response.Body.Data[0].Embedding
		vec := make([]float32, len(raw))
		for j, v := range raw {
			vec[j] = float32(v)
		}
		byIndex[idx] = vec
	}

	out := make([][]float32, len(inputs))
	for i := range inputs {
		vec, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("openai batch: missing embedding for input %d", i)
		}
		out[i] = vec
	}
	return out, nil
}

// doJSON executes a request expecting a 200 JSON body, decoding it into out.
func (e *BatchEmbedder) doJSON(req *http.Request, out any) error {
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// BatchDistiller distills a slice of rendered conversations via the Anthropic
// Message Batches API. DistillBatch blocks until the batch completes and
// returns the parsed observations per conversation in input order. It reuses
// distillRequestBody + parseObservations from anthropic.go so the tool schema
// and system prompt are not duplicated.
type BatchDistiller struct {
	client *http.Client
	apiKey string
	model  string

	// BaseURL is the API root (default https://api.anthropic.com/v1); tests
	// point it at an httptest server.
	BaseURL string

	// PollInterval is the sleep between batch-status polls (default 5s).
	PollInterval time.Duration
}

func NewBatchDistiller(client *http.Client, apiKey, model string) *BatchDistiller {
	return &BatchDistiller{
		client:       client,
		apiKey:       apiKey,
		model:        model,
		BaseURL:      anthropicBatchBaseURL,
		PollInterval: defaultBatchPollInterval,
	}
}

func (d *BatchDistiller) Configured() bool { return d.apiKey != "" }

// DistillBatch distills every conversation via the Anthropic Message Batches
// API, returning the observations per conversation in input order. A single
// conversation whose result is not "succeeded" yields an empty slice for that
// index rather than failing the whole batch.
func (d *BatchDistiller) DistillBatch(ctx context.Context, conversations []string) ([][]string, error) {
	if len(conversations) == 0 {
		return nil, nil
	}

	batchID, err := d.createBatch(ctx, conversations)
	if err != nil {
		return nil, err
	}

	resultsURL, err := d.pollBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}

	body, err := d.fetchResults(ctx, resultsURL)
	if err != nil {
		return nil, err
	}

	return d.parseResults(body, conversations)
}

// createBatch submits all conversations as one Message Batches request and
// returns its id.
func (d *BatchDistiller) createBatch(ctx context.Context, conversations []string) (string, error) {
	requests := make([]map[string]any, len(conversations))
	for i, conv := range conversations {
		requests[i] = map[string]any{
			"custom_id": "dis-" + strconv.Itoa(i),
			"params":    distillRequestBody(d.model, conv),
		}
	}
	reqBody, err := json.Marshal(map[string]any{"requests": requests})
	if err != nil {
		return "", fmt.Errorf("anthropic batch: marshal create request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/messages/batches", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("anthropic batch: build create request: %w", err)
	}
	d.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		ID               string `json:"id"`
		ProcessingStatus string `json:"processing_status"`
	}
	if err := d.doJSON(req, &out); err != nil {
		return "", fmt.Errorf("anthropic batch: create batch: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("anthropic batch: create batch: empty batch id in response")
	}
	return out.ID, nil
}

// pollBatch polls until processing_status == "ended", returning the results
// URL.
func (d *BatchDistiller) pollBatch(ctx context.Context, batchID string) (string, error) {
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BaseURL+"/messages/batches/"+batchID, nil)
		if err != nil {
			return "", fmt.Errorf("anthropic batch: build poll request: %w", err)
		}
		d.setHeaders(req)

		var out struct {
			ProcessingStatus string `json:"processing_status"`
			ResultsURL       string `json:"results_url"`
		}
		if err := d.doJSON(req, &out); err != nil {
			return "", fmt.Errorf("anthropic batch: poll batch: %w", err)
		}

		if out.ProcessingStatus == "ended" {
			if out.ResultsURL == "" {
				return "", fmt.Errorf("anthropic batch: ended batch has no results_url")
			}
			return out.ResultsURL, nil
		}

		if err := sleepCtx(ctx, d.PollInterval); err != nil {
			return "", err
		}
	}
}

// fetchResults downloads the JSONL results body from the results URL.
func (d *BatchDistiller) fetchResults(ctx context.Context, resultsURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resultsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: build results request: %w", err)
	}
	d.setHeaders(req)

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: fetch results: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return nil, fmt.Errorf("anthropic batch: fetch results: unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: read results: %w", err)
	}
	return body, nil
}

// parseResults maps each result line's custom_id to its conversation index and
// reassembles the observations in input order. A non-"succeeded" result yields
// an empty slice for that index (the batch still succeeds overall).
func (d *BatchDistiller) parseResults(body []byte, conversations []string) ([][]string, error) {
	byIndex := make(map[int][]string, len(conversations))
	dec := json.NewDecoder(bytes.NewReader(body))
	for dec.More() {
		var line struct {
			CustomID string `json:"custom_id"`
			Result   struct {
				Type    string `json:"type"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			} `json:"result"`
		}
		if err := dec.Decode(&line); err != nil {
			return nil, fmt.Errorf("anthropic batch: decode result line: %w", err)
		}
		idx, err := customIndex(line.CustomID, "dis-")
		if err != nil {
			return nil, fmt.Errorf("anthropic batch: %w", err)
		}
		if line.Result.Type != "succeeded" {
			// errored/expired/canceled: no observations for this
			// conversation, but don't fail the whole batch.
			byIndex[idx] = []string{}
			continue
		}
		byIndex[idx] = parseObservations(line.Result.Message.Content)
	}

	out := make([][]string, len(conversations))
	for i := range conversations {
		obs, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("anthropic batch: missing result for conversation %d", i)
		}
		out[i] = obs
	}
	return out, nil
}

// setHeaders attaches the Anthropic auth + version + batch-beta headers.
func (d *BatchDistiller) setHeaders(req *http.Request) {
	req.Header.Set("x-api-key", d.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("anthropic-beta", anthropicBatchBeta)
}

// doJSON executes a request expecting a 200 JSON body, decoding it into out.
func (d *BatchDistiller) doJSON(req *http.Request, out any) error {
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// customIndex parses the trailing integer from a "<prefix><i>" custom_id.
func customIndex(customID, prefix string) (int, error) {
	if !strings.HasPrefix(customID, prefix) {
		return 0, fmt.Errorf("unexpected custom_id %q (want prefix %q)", customID, prefix)
	}
	idx, err := strconv.Atoi(strings.TrimPrefix(customID, prefix))
	if err != nil {
		return 0, fmt.Errorf("bad custom_id %q: %w", customID, err)
	}
	return idx, nil
}

// sleepCtx sleeps for d or returns the context error if ctx is canceled
// first — so a long backfill respects SIGINT.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
