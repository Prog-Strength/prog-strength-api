package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTurnHandler_PersistsIntent(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t) // defined in sqlite_repository_test.go
	defer cleanup()
	h := NewHandler(repo)

	body := strings.NewReader(`{
		"id": "turn-1",
		"user_id": "u-1",
		"session_id": "s-1",
		"model": "claude-haiku-4-5-20251001",
		"routed_tier": "simple",
		"router_model": "claude-haiku-4-5-20251001",
		"router_latency_ms": 400,
		"completion_reason": "end_turn",
		"started_at": "2026-06-02T00:00:00Z",
		"ended_at":   "2026-06-02T00:00:01Z",
		"intent": "log_nutrition",
		"intent_prefetch_duration_ms": 87,
		"intent_prefetch_failed": false
	}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/turns", body)
	w := httptest.NewRecorder()

	h.turn(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201, body=%s", w.Code, w.Body.String())
	}

	// Verify the row landed in the DB with intent fields populated.
	var (
		gotIntent           string
		gotPrefetchDuration int
		gotPrefetchFailed   int
	)
	err := repo.db.QueryRow(
		`SELECT intent, intent_prefetch_duration_ms, intent_prefetch_failed
		   FROM agent_turns WHERE id = ?`, "turn-1",
	).Scan(&gotIntent, &gotPrefetchDuration, &gotPrefetchFailed)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotIntent != "log_nutrition" || gotPrefetchDuration != 87 || gotPrefetchFailed != 0 {
		t.Fatalf("turn not persisted with intent fields: intent=%q dur=%d failed=%d", gotIntent, gotPrefetchDuration, gotPrefetchFailed)
	}
}
