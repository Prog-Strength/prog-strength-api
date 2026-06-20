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

func TestOpenAIEmbedOrdersByIndexAndConvertsToFloat32(t *testing.T) {
	var gotBody struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		// Return out of order by index to exercise the defensive sort.
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.4, 0.5, 0.6}, "index": 1},
				{"embedding": []float64{0.1, 0.2, 0.3}, "index": 0},
			},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	e := NewOpenAIEmbedder(srv.Client(), "sk-test", "text-embedding-3-small")
	e.BaseURL = srv.URL

	vecs, err := e.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	want := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	if !reflect.DeepEqual(vecs, want) {
		t.Errorf("vectors = %v, want %v (ordered by index)", vecs, want)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer sk-test")
	}
	if gotBody.Model != "text-embedding-3-small" {
		t.Errorf("request model = %q, want text-embedding-3-small", gotBody.Model)
	}
	if !reflect.DeepEqual(gotBody.Input, []string{"first", "second"}) {
		t.Errorf("request input = %v, want [first second]", gotBody.Input)
	}
}

func TestOpenAIEmbedEmptyInputSkipsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be called for empty input, got %s", r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	e := NewOpenAIEmbedder(srv.Client(), "sk-test", "text-embedding-3-small")
	e.BaseURL = srv.URL

	vecs, err := e.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("len(vecs) = %d, want 0", len(vecs))
	}
}

func TestOpenAIEmbedNon200Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(srv.Close)

	e := NewOpenAIEmbedder(srv.Client(), "sk-test", "text-embedding-3-small")
	e.BaseURL = srv.URL

	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed: expected error on 429, got nil")
	}
}

func TestOpenAIConfigured(t *testing.T) {
	if NewOpenAIEmbedder(http.DefaultClient, "", "m").Configured() {
		t.Error("Configured() = true with empty api key, want false")
	}
	if !NewOpenAIEmbedder(http.DefaultClient, "sk-test", "m").Configured() {
		t.Error("Configured() = false with api key set, want true")
	}
}
