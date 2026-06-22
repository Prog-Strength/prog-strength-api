package vectormemory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAnthropicDistillParsesToolUse(t *testing.T) {
	var gotBody struct {
		Model      string `json:"model"`
		MaxTokens  int    `json:"max_tokens"`
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	var gotAPIKey, gotVersion string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","name":"record_observations","input":{"observations":["Trains in hotel gyms.","Left shoulder flares on overhead pressing."]}}],"usage":{"input_tokens":412,"output_tokens":37}}`))
	}))
	t.Cleanup(srv.Close)

	d := NewAnthropicDistiller(srv.Client(), "key-123", "claude-test")
	d.BaseURL = srv.URL

	got, usage, err := d.Distill(context.Background(), "user and coach talk", "")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}

	want := []string{"Trains in hotel gyms.", "Left shoulder flares on overhead pressing."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("observations = %v, want %v", got, want)
	}
	if usage.InputTokens != 412 || usage.OutputTokens != 37 {
		t.Errorf("usage = %+v, want {InputTokens:412 OutputTokens:37}", usage)
	}
	if gotAPIKey != "key-123" {
		t.Errorf("x-api-key = %q, want key-123", gotAPIKey)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", gotVersion)
	}
	if gotBody.Model != "claude-test" {
		t.Errorf("request model = %q, want claude-test", gotBody.Model)
	}
	if gotBody.MaxTokens != 1024 {
		t.Errorf("max_tokens = %d, want 1024", gotBody.MaxTokens)
	}
	if gotBody.ToolChoice.Name != "record_observations" {
		t.Errorf("tool_choice.name = %q, want record_observations", gotBody.ToolChoice.Name)
	}
}

func TestAnthropicDistillEmptyObservations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","name":"record_observations","input":{"observations":[]}}]}`))
	}))
	t.Cleanup(srv.Close)

	d := NewAnthropicDistiller(srv.Client(), "key-123", "claude-test")
	d.BaseURL = srv.URL

	got, _, err := d.Distill(context.Background(), "nothing durable here", "")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("observations = %v, want empty", got)
	}
}

func TestAnthropicDistillTextOnlyNoToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"I have nothing to record."}]}`))
	}))
	t.Cleanup(srv.Close)

	d := NewAnthropicDistiller(srv.Client(), "key-123", "claude-test")
	d.BaseURL = srv.URL

	got, _, err := d.Distill(context.Background(), "chit chat", "")
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("observations = %v, want empty (no tool_use)", got)
	}
}

func TestAnthropicDistillNon200Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	t.Cleanup(srv.Close)

	d := NewAnthropicDistiller(srv.Client(), "key-123", "claude-test")
	d.BaseURL = srv.URL

	if _, _, err := d.Distill(context.Background(), "x", ""); err == nil {
		t.Fatal("Distill: expected error on 500, got nil")
	}
}

// TestDistillRequestBodyPromptHint is the proof that chat behavior is
// unchanged: an empty (or whitespace-only) promptHint produces a request body
// whose system prompt is EXACTLY distillSystemPrompt, while a non-empty hint is
// appended after a blank-line separator. The rest of the body is identical
// either way.
func TestDistillRequestBodyPromptHint(t *testing.T) {
	t.Run("empty hint leaves the system prompt unchanged", func(t *testing.T) {
		body := distillRequestBody("m", "conv", "")
		if got := body["system"]; got != distillSystemPrompt {
			t.Fatalf("system = %q, want exactly distillSystemPrompt", got)
		}
	})

	t.Run("whitespace-only hint leaves the system prompt unchanged", func(t *testing.T) {
		body := distillRequestBody("m", "conv", "   \n\t ")
		if got := body["system"]; got != distillSystemPrompt {
			t.Fatalf("system = %q, want exactly distillSystemPrompt (whitespace hint ignored)", got)
		}
	})

	t.Run("non-empty hint is appended after a blank line", func(t *testing.T) {
		hint := "Notes are terse training-log shorthand."
		body := distillRequestBody("m", "conv", hint)
		want := distillSystemPrompt + "\n\n" + hint
		if got := body["system"]; got != want {
			t.Fatalf("system = %q, want %q", got, want)
		}
	})

	t.Run("empty-hint body keeps the system prompt unchanged through a JSON round-trip", func(t *testing.T) {
		got, err := json.Marshal(distillRequestBody("claude-x", "a transcript", ""))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded struct {
			System string `json:"system"`
		}
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.System != distillSystemPrompt {
			t.Fatalf("round-tripped system = %q, want distillSystemPrompt", decoded.System)
		}
	})
}

func TestAnthropicConfigured(t *testing.T) {
	if NewAnthropicDistiller(http.DefaultClient, "", "m").Configured() {
		t.Error("Configured() = true with empty api key, want false")
	}
	if !NewAnthropicDistiller(http.DefaultClient, "key", "m").Configured() {
		t.Error("Configured() = false with api key set, want true")
	}
}
