package nutritionlookup

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
)

// testLogger discards all records — logging behavior itself is pinned
// in this file; everywhere else the logger is plumbing.
func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// ctxWithRequestID runs a no-op request through the real requestid
// middleware and captures the resulting context — the same path
// production requests take, so the test can't drift from the
// middleware's storage scheme.
func ctxWithRequestID(t *testing.T, id string) context.Context {
	t.Helper()
	var ctx context.Context
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx = r.Context()
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(requestid.HeaderName, id)
	requestid.Middleware(next).ServeHTTP(httptest.NewRecorder(), r)
	return ctx
}

func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("non-JSON log line %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func TestLoggerStampsRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelDebug)

	logger.InfoContext(ctxWithRequestID(t, "req-abc-123"), "cache hit", "query", "big mac")

	records := logLines(t, &buf)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if got := records[0]["request_id"]; got != "req-abc-123" {
		t.Errorf("request_id = %v, want req-abc-123", got)
	}
	if got := records[0]["query"]; got != "big mac" {
		t.Errorf("query attr = %v, want big mac", got)
	}
}

func TestLoggerOmitsRequestIDOnBareContext(t *testing.T) {
	// Startup and test code paths log with contexts that never passed
	// through the middleware — no attribute, no panic.
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelDebug)

	logger.InfoContext(context.Background(), "lookup unavailable")

	records := logLines(t, &buf)
	if _, present := records[0]["request_id"]; present {
		t.Errorf("request_id present on bare context: %v", records[0])
	}
}

func TestLoggerStampingSurvivesWith(t *testing.T) {
	// Logger.With derives a new handler via WithAttrs; the request-id
	// wrapper must survive the derivation.
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelDebug).With("source", "fatsecret")

	logger.DebugContext(ctxWithRequestID(t, "req-with"), "provider search ok")

	records := logLines(t, &buf)
	if got := records[0]["request_id"]; got != "req-with" {
		t.Errorf("request_id = %v, want req-with (lost through With)", got)
	}
	if got := records[0]["source"]; got != "fatsecret" {
		t.Errorf("source = %v, want fatsecret", got)
	}
}

func TestLoggerLevelGatesDebug(t *testing.T) {
	// LOG_LEVEL=info must suppress the verbose records (cache write ok,
	// provider HTTP details) while keeping the INFO summaries — the
	// steady-state configuration once development quiets down.
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	ctx := ctxWithRequestID(t, "req-lvl")

	logger.DebugContext(ctx, "cache write ok")
	logger.InfoContext(ctx, "lookup request served")

	records := logLines(t, &buf)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (debug suppressed at info)", len(records))
	}
	if got := records[0]["msg"]; got != "lookup request served" {
		t.Errorf("surviving record = %v", got)
	}
}

func TestServiceLogsProviderMergeAndCandidatesAtInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	ctx := ctxWithRequestID(t, "req-merge")
	fs := &fakeProvider{
		source:     "fatsecret",
		configured: true,
		hits: []Candidate{newCandidate(
			"Halo Top", "Halo Top", "1 container",
			Macros{Calories: 320, ProteinG: 20, FatG: 8, CarbsG: 48},
			"fatsecret", "123",
		)},
	}
	usda := &fakeProvider{source: "usda", configured: true}
	svc := NewService(NewSQLiteRepository(dbtest.New(t)), logger, fs, usda)

	if _, err := svc.Lookup(ctx, "halo top", 1, 5); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	records := logLines(t, &buf)
	msgs := recordMessages(records)
	for _, want := range []string{
		"provider search ok",
		"lookup provider merge",
		"lookup served",
		"lookup candidates",
	} {
		if !containsMessage(msgs, want) {
			t.Errorf("missing INFO log %q; got %v", want, msgs)
		}
	}
	for _, record := range records {
		if record["msg"] == "lookup provider merge" {
			if record["macro_selection"] != "api_returns_candidates_agent_chooses" {
				t.Errorf("merge macro_selection = %v", record["macro_selection"])
			}
			if record["providers_queried"] != "fatsecret,usda" {
				t.Errorf("providers_queried = %v, want fatsecret,usda", record["providers_queried"])
			}
		}
		if record["msg"] == "lookup candidates" {
			summary, ok := record["matches_summary"].(string)
			if !ok || !strings.Contains(summary, "fatsecret/123") {
				t.Errorf("matches_summary = %v, want fatsecret candidate", record["matches_summary"])
			}
		}
	}
}

func TestServiceLookupDetailGatedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	ctx := ctxWithRequestID(t, "req-info-only")
	fs := &fakeProvider{
		source:     "fatsecret",
		configured: true,
		hits:       []Candidate{fsCandidate("Eggs", Macros{Calories: 70, ProteinG: 6, FatG: 5, CarbsG: 0})},
	}
	svc := NewService(NewSQLiteRepository(dbtest.New(t)), logger, fs)

	if _, err := svc.Lookup(ctx, "eggs", 1, 5); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	for _, record := range logLines(t, &buf) {
		if strings.HasSuffix(record["msg"].(string), " detail") {
			t.Errorf("debug detail leaked at info level: %v", record["msg"])
		}
	}
}

func recordMessages(records []map[string]any) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		if msg, ok := r["msg"].(string); ok {
			out = append(out, msg)
		}
	}
	return out
}

func containsMessage(msgs []string, target string) bool {
	for _, msg := range msgs {
		if msg == target {
			return true
		}
	}
	return false
}

func TestServiceLogsCacheHitWithRequestID(t *testing.T) {
	// One end-to-end pin: a fresh-cache lookup emits the "cache hit"
	// INFO record carrying the request id — the exact line the
	// CloudWatch filter-by-request-id workflow depends on.
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelDebug)
	repo := NewSQLiteRepository(dbtest.New(t))
	svc := NewService(repo, logger, &fakeProvider{})
	ctx := ctxWithRequestID(t, "req-cache-hit")

	if err := repo.Put(ctx, CacheRow{
		QueryNormalized: "big mac",
		CandidatesJSON:  `[{"name":"Big Mac","per_serving":{"calories":590,"protein_g":25,"fat_g":34,"carbs_g":46},"source":"fatsecret"}]`,
		FetchedAt:       svc.now().UTC(),
		LastUsedAt:      svc.now().UTC(),
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	if _, err := svc.Lookup(ctx, "Big  Mac", 2, 5); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	var hit map[string]any
	for _, record := range logLines(t, &buf) {
		if record["msg"] == "cache hit" {
			hit = record
		}
	}
	if hit == nil {
		t.Fatal("no 'cache hit' record logged")
	}
	if hit["request_id"] != "req-cache-hit" {
		t.Errorf("cache hit request_id = %v", hit["request_id"])
	}
	if hit["query"] != "big mac" {
		t.Errorf("cache hit query = %v (want normalized form)", hit["query"])
	}
}
