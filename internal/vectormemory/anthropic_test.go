package vectormemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

// TestAnthropicDistillTerminalStatusClassification pins which provider failures
// the job may retry. A rejection of the request itself will be rejected
// identically forever, so it is tagged ErrUnitUnprocessable and the unit leaves
// the queue; anything that can succeed on a later attempt must NOT carry the
// tag, or a rate limit would silently discard real conversations.
func TestAnthropicDistillTerminalStatusClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		terminal bool
	}{
		{"400 invalid request", http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"messages.0: user messages must have non-empty content"}}`, true},
		{"404 unknown model", http.StatusNotFound, `{"error":{"type":"not_found_error"}}`, true},
		{"413 payload too large", http.StatusRequestEntityTooLarge, `{"error":{"type":"request_too_large"}}`, true},
		{"401 bad credentials", http.StatusUnauthorized, `{"error":{"type":"authentication_error"}}`, false},
		{"403 forbidden", http.StatusForbidden, `{"error":{"type":"permission_error"}}`, false},
		{"408 request timeout", http.StatusRequestTimeout, `{"error":{"type":"timeout_error"}}`, false},
		{"429 rate limited", http.StatusTooManyRequests, `{"error":{"type":"rate_limit_error"}}`, false},
		{"500 server error", http.StatusInternalServerError, `{"error":{"type":"api_error"}}`, false},
		{"529 overloaded", 529, `{"error":{"type":"overloaded_error"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			d := NewAnthropicDistiller(srv.Client(), "key-123", "claude-test")
			d.BaseURL = srv.URL

			_, _, err := d.Distill(context.Background(), "x", "")
			if err == nil {
				t.Fatalf("Distill: expected an error on %d, got nil", tc.status)
			}
			if got := errors.Is(err, ErrUnitUnprocessable); got != tc.terminal {
				t.Fatalf("errors.Is(err, ErrUnitUnprocessable) = %v, want %v (err: %v)", got, tc.terminal, err)
			}
			// The status and provider message must survive the wrapping — they
			// are what an operator reads in CloudWatch.
			if !strings.Contains(err.Error(), fmt.Sprintf("status %d", tc.status)) {
				t.Fatalf("error lost the status code: %v", err)
			}
		})
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
