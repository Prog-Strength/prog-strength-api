package vectormemory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestEmbedBatchLifecycle drives BatchEmbedder against a stateful fake of the
// OpenAI Batch API: file upload → batch create → in_progress then completed →
// output file content. It asserts vectors come back in input order (via
// custom_id reassembly) as float32, even when the output lines are shuffled.
func TestEmbedBatchLifecycle(t *testing.T) {
	var pollCount int
	var gotPurpose, gotAuth string

	mux := http.NewServeMux()
	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotPurpose = r.FormValue("purpose")
		writeJSON(t, w, `{"id":"file-in-1"}`)
	})
	mux.HandleFunc("/batches", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"id":"batch-1","status":"validating"}`)
	})
	mux.HandleFunc("/batches/batch-1", func(w http.ResponseWriter, _ *http.Request) {
		pollCount++
		if pollCount == 1 {
			writeJSON(t, w, `{"status":"in_progress"}`)
			return
		}
		writeJSON(t, w, `{"status":"completed","output_file_id":"file-out-1"}`)
	})
	mux.HandleFunc("/files/file-out-1/content", func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately out of order: emb-1 first, then emb-0.
		writeJSONL(t, w,
			`{"custom_id":"emb-1","response":{"status_code":200,"body":{"data":[{"embedding":[0.4,0.5,0.6],"index":0}]}}}`,
			`{"custom_id":"emb-0","response":{"status_code":200,"body":{"data":[{"embedding":[0.1,0.2,0.3],"index":0}]}}}`,
		)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	e := NewBatchEmbedder(srv.Client(), "key-xyz", "text-embedding-3-small")
	e.BaseURL = srv.URL
	e.PollInterval = time.Millisecond

	got, err := e.EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}

	want := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vectors = %v, want %v", got, want)
	}
	if pollCount < 2 {
		t.Errorf("pollCount = %d, want >= 2 (in_progress then completed)", pollCount)
	}
	if gotPurpose != "batch" {
		t.Errorf("upload purpose = %q, want batch", gotPurpose)
	}
	if gotAuth != "Bearer key-xyz" {
		t.Errorf("auth header = %q, want Bearer key-xyz", gotAuth)
	}
}

func TestEmbedBatchEmptyInput(t *testing.T) {
	e := NewBatchEmbedder(http.DefaultClient, "k", "m")
	got, err := e.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for empty input", got)
	}
}

func TestEmbedBatchTerminalFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/files", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"id":"file-in-1"}`)
	})
	mux.HandleFunc("/batches", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"id":"batch-1","status":"validating"}`)
	})
	mux.HandleFunc("/batches/batch-1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, `{"status":"failed"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	e := NewBatchEmbedder(srv.Client(), "k", "m")
	e.BaseURL = srv.URL
	e.PollInterval = time.Millisecond

	if _, err := e.EmbedBatch(context.Background(), []string{"x"}); err == nil {
		t.Fatal("EmbedBatch: expected error on failed status, got nil")
	}
}

// TestDistillBatchLifecycle drives BatchDistiller against a stateful fake of
// the Anthropic Message Batches API: create → in_progress then ended → results
// JSONL. It asserts observations come back per conversation in input order, and
// that a non-"succeeded" result line yields an empty slice without erroring.
func TestDistillBatchLifecycle(t *testing.T) {
	var pollCount int
	var gotBeta, gotVersion, gotKey string
	var gotRequests int
	var srvURL string // closed over to build an absolute results_url

	mux := http.NewServeMux()
	mux.HandleFunc("/messages/batches", func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		body, _ := io.ReadAll(r.Body)
		var parsed struct {
			Requests []json.RawMessage `json:"requests"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Errorf("unmarshal create body: %v", err)
		}
		gotRequests = len(parsed.Requests)
		writeJSON(t, w, `{"id":"msgbatch_1","processing_status":"in_progress"}`)
	})
	mux.HandleFunc("/messages/batches/msgbatch_1", func(w http.ResponseWriter, r *http.Request) {
		// Only the GET poll (the create path is /messages/batches exactly).
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		pollCount++
		if pollCount == 1 {
			writeJSON(t, w, `{"processing_status":"in_progress"}`)
			return
		}
		writeJSON(t, w, `{"processing_status":"ended","results_url":"`+srvURL+`/results"}`)
	})
	mux.HandleFunc("/results", func(w http.ResponseWriter, _ *http.Request) {
		// Out of order; index 1 is an errored result → empty slice.
		writeJSONL(t, w,
			`{"custom_id":"dis-0","result":{"type":"succeeded","message":{"content":[{"type":"tool_use","name":"record_observations","input":{"observations":["Trains in hotel gyms.","  ","Cutting for a meet."]}}]}}}`,
			`{"custom_id":"dis-1","result":{"type":"errored","error":{"type":"invalid_request"}}}`,
		)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	srvURL = srv.URL

	d := NewBatchDistiller(srv.Client(), "key-abc", "claude-test")
	d.BaseURL = srv.URL
	d.PollInterval = time.Millisecond

	got, err := d.DistillBatch(context.Background(), []string{"conv one", "conv two"})
	if err != nil {
		t.Fatalf("DistillBatch: %v", err)
	}

	want := [][]string{
		{"Trains in hotel gyms.", "Cutting for a meet."},
		{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("observations = %#v, want %#v", got, want)
	}
	if pollCount < 2 {
		t.Errorf("pollCount = %d, want >= 2", pollCount)
	}
	if gotRequests != 2 {
		t.Errorf("submitted requests = %d, want 2", gotRequests)
	}
	if gotBeta != anthropicBatchBeta {
		t.Errorf("anthropic-beta = %q, want %q", gotBeta, anthropicBatchBeta)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, anthropicVersion)
	}
	if gotKey != "key-abc" {
		t.Errorf("x-api-key = %q, want key-abc", gotKey)
	}
}

func TestDistillBatchEmptyInput(t *testing.T) {
	d := NewBatchDistiller(http.DefaultClient, "k", "m")
	got, err := d.DistillBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("DistillBatch: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for empty input", got)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write json: %v", err)
	}
}

func writeJSONL(t *testing.T, w http.ResponseWriter, lines ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/x-ndjson")
	if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Errorf("write jsonl: %v", err)
	}
}
