package vectormemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Anthropic distillation provider.
//
// why: a conversation between the user and their coach is read by the
// model and reduced to atomic durable facts worth remembering. Output is
// forced through a record_observations tool so the response is always a
// (possibly empty) JSON array — never prose we'd have to parse loosely.

const anthropicMessagesURL = "https://api.anthropic.com/v1/messages"

// anthropicVersion is the dated API version header Anthropic requires.
const anthropicVersion = "2023-06-01"

// distillMaxTokens caps the tool-call output; observations are short and
// few, so a small budget is plenty.
const distillMaxTokens = 1024

// distillToolName is the forced tool the model must call.
const distillToolName = "record_observations"

// distillSystemPrompt steers the model toward durable signal only.
//
// why: this is a first draft. The exact wording — what counts as
// "durable" vs transient — will be tuned against real conversations via
// the admin distillation probe (SOW Open Q2) rather than guessed at now.
const distillSystemPrompt = `You extract durable, long-term facts about a fitness app user from a conversation between the user and their AI coach. Record ONLY stable, durable signal worth remembering across future conversations: training constraints (travel, schedule, equipment/gym access), injuries or physical limitations, long-term goals (e.g. cutting for an event), dietary patterns or restrictions, and strong stated preferences or dislikes. Do NOT record one-off logging actions ("logged a workout"), transient state, app mechanics, or anything already obvious from structured data. Each observation must be a single atomic fact, phrased as a standalone third-person statement (e.g. "Travels for work most weeks and trains in hotel gyms."). If the conversation holds nothing durable, return an empty array. Always call record_observations.`

// Compile-time check that *AnthropicDistiller satisfies Distiller.
var _ Distiller = (*AnthropicDistiller)(nil)

type AnthropicDistiller struct {
	client *http.Client
	apiKey string
	model  string

	// BaseURL defaults to the production messages endpoint; tests point
	// it at an httptest server.
	BaseURL string
}

func NewAnthropicDistiller(client *http.Client, apiKey, model string) *AnthropicDistiller {
	return &AnthropicDistiller{client: client, apiKey: apiKey, model: model, BaseURL: anthropicMessagesURL}
}

func (d *AnthropicDistiller) Configured() bool { return d.apiKey != "" }

// distillRequestBody builds the Anthropic message-create params that force the
// model through the record_observations tool. Shared by the synchronous
// AnthropicDistiller and the batch BatchDistiller so the tool schema and
// system prompt live in exactly one place. promptHint is a per-source framing
// string appended to the system prompt (separated by a blank line) only when
// non-empty — an empty hint produces exactly the original request body, so chat
// behavior is byte-for-byte unchanged.
func distillRequestBody(model, conversation, promptHint string) map[string]any {
	system := distillSystemPrompt
	if strings.TrimSpace(promptHint) != "" {
		system = distillSystemPrompt + "\n\n" + promptHint
	}
	return map[string]any{
		"model":      model,
		"max_tokens": distillMaxTokens,
		"system":     system,
		"messages": []map[string]any{
			{"role": "user", "content": conversation},
		},
		"tools": []map[string]any{{
			"name":        distillToolName,
			"description": "Record durable observations worth remembering about the user.",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"observations": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Atomic durable observations; empty if none.",
					},
				},
				"required": []string{"observations"},
			},
		}},
		"tool_choice": map[string]any{"type": "tool", "name": distillToolName},
	}
}

// parseObservations extracts the observations array from a message's content
// blocks (the JSON array under a record_observations tool_use), trimming and
// dropping empties. A response with no matching tool_use block yields an empty
// slice — not an error — because the model declining is a valid "nothing
// durable" outcome. Shared by both the synchronous and batch distillers.
func parseObservations(content json.RawMessage) []string {
	var blocks []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Input struct {
			Observations []string `json:"observations"`
		} `json:"input"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return []string{}
	}
	for _, block := range blocks {
		if block.Type != "tool_use" || block.Name != distillToolName {
			continue
		}
		out := make([]string, 0, len(block.Input.Observations))
		for _, obs := range block.Input.Observations {
			if trimmed := strings.TrimSpace(obs); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}
	return []string{}
}

func (d *AnthropicDistiller) Distill(ctx context.Context, content, promptHint string) ([]string, DistillUsage, error) {
	reqBody, err := json.Marshal(distillRequestBody(d.model, content, promptHint))
	if err != nil {
		return nil, DistillUsage{}, fmt.Errorf("anthropic distill: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, DistillUsage{}, fmt.Errorf("anthropic distill: build request: %w", err)
	}
	req.Header.Set("x-api-key", d.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, DistillUsage{}, fmt.Errorf("anthropic distill: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return nil, DistillUsage{}, fmt.Errorf("anthropic distill: unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}

	// Usage is parsed alongside the content so the distillation job can meter
	// token spend; an absent usage block decodes to a zero-value DistillUsage
	// rather than failing the call.
	var payload struct {
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, DistillUsage{}, fmt.Errorf("anthropic distill: decode response: %w", err)
	}

	usage := DistillUsage{
		InputTokens:  payload.Usage.InputTokens,
		OutputTokens: payload.Usage.OutputTokens,
	}
	// parseObservations returns an empty slice when there is no tool_use
	// block (the model declined with text only) — that is "nothing durable
	// to record", not an error.
	return parseObservations(payload.Content), usage, nil
}
