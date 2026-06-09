package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		"intent_prefetch_failed": false,
		"had_image": true
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
		gotHadImage         int
	)
	err := repo.db.QueryRow(
		`SELECT intent, intent_prefetch_duration_ms, intent_prefetch_failed, had_image
		   FROM agent_turns WHERE id = ?`, "turn-1",
	).Scan(&gotIntent, &gotPrefetchDuration, &gotPrefetchFailed, &gotHadImage)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotIntent != "log_nutrition" || gotPrefetchDuration != 87 || gotPrefetchFailed != 0 || gotHadImage != 1 {
		t.Fatalf("turn not persisted with intent fields: intent=%q dur=%d failed=%d had_image=%d", gotIntent, gotPrefetchDuration, gotPrefetchFailed, gotHadImage)
	}
}

// TestTurnHandler_HadImageDefaultsFalse confirms a body that omits
// had_image (an older agent client) persists 0 via the Go zero value
// and column default rather than 400ing.
func TestTurnHandler_HadImageDefaultsFalse(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()
	h := NewHandler(repo)

	body := strings.NewReader(`{
		"id": "turn-noimg",
		"user_id": "u-1",
		"session_id": "s-1",
		"model": "claude-haiku-4-5-20251001",
		"routed_tier": "simple",
		"router_model": "claude-haiku-4-5-20251001",
		"router_latency_ms": 400,
		"completion_reason": "end_turn",
		"started_at": "2026-06-02T00:00:00Z",
		"ended_at":   "2026-06-02T00:00:01Z",
		"intent": "general"
	}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/turns", body)
	w := httptest.NewRecorder()

	h.turn(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201, body=%s", w.Code, w.Body.String())
	}

	var gotHadImage int
	if err := repo.db.QueryRow(
		`SELECT had_image FROM agent_turns WHERE id = ?`, "turn-noimg",
	).Scan(&gotHadImage); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotHadImage != 0 {
		t.Fatalf("had_image: got %d want 0", gotHadImage)
	}
}

func TestSpeakHandler_PersistsRowAnd204(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()
	h := NewHandler(repo)

	body := strings.NewReader(`{
		"id": "sp-1",
		"user_id": "u-1",
		"session_id": "s-1",
		"model": "gpt-4o-mini-tts",
		"chars": 184,
		"voice": "alloy",
		"started_at": "2026-06-09T18:22:10Z",
		"ended_at":   "2026-06-09T18:22:11Z",
		"error": null
	}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/speak", body)
	w := httptest.NewRecorder()
	h.speak(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d want 204, body=%s", w.Code, w.Body.String())
	}

	var (
		gotUser  string
		gotChars int64
		gotVoice string
	)
	if err := repo.db.QueryRow(
		`SELECT user_id, chars, voice FROM agent_speak_calls WHERE id = ?`, "sp-1",
	).Scan(&gotUser, &gotChars, &gotVoice); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotUser != "u-1" || gotChars != 184 || gotVoice != "alloy" {
		t.Fatalf("row mismatch: user=%q chars=%d voice=%q", gotUser, gotChars, gotVoice)
	}
}

func TestSpeakHandler_NullSessionAccepted(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()
	h := NewHandler(repo)

	body := strings.NewReader(`{
		"id": "sp-2",
		"user_id": "u-1",
		"session_id": null,
		"model": "tts-1",
		"chars": 42,
		"voice": "verse",
		"started_at": "2026-06-09T18:22:10Z",
		"ended_at":   "2026-06-09T18:22:11Z",
		"error": null
	}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/speak", body)
	w := httptest.NewRecorder()
	h.speak(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d want 204, body=%s", w.Code, w.Body.String())
	}
}

func TestSpeakHandler_MalformedBody400(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()
	h := NewHandler(repo)

	// Missing required model/voice and a bad timestamp.
	body := strings.NewReader(`{"user_id": "u-1"}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/speak", body)
	w := httptest.NewRecorder()
	h.speak(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestSpeakHandler_InvalidTimestamp400(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()
	h := NewHandler(repo)

	body := strings.NewReader(`{
		"id": "sp-3",
		"user_id": "u-1",
		"model": "tts-1",
		"chars": 10,
		"voice": "alloy",
		"started_at": "not-a-time",
		"ended_at": "2026-06-09T18:22:11Z"
	}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/speak", body)
	w := httptest.NewRecorder()
	h.speak(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", w.Code)
	}
}

type fakeIntentSink struct {
	calls []intentSinkCall
}

type intentSinkCall struct {
	sessionID string
	intent    string
	at        time.Time
}

func (s *fakeIntentSink) SetSessionIntent(ctx context.Context, sessionID, intent string, at time.Time) error {
	s.calls = append(s.calls, intentSinkCall{sessionID, intent, at})
	return nil
}

func TestTurnHandler_WritesLastIntentForNonGeneral(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()
	sink := &fakeIntentSink{}
	h := NewHandlerWithIntentSink(repo, sink)

	body := strings.NewReader(`{
		"id": "turn-1",
		"user_id": "u-1",
		"session_id": "11111111-2222-4333-8444-555555555555",
		"model": "claude-haiku-4-5-20251001",
		"routed_tier": "simple",
		"router_model": "claude-haiku-4-5-20251001",
		"router_latency_ms": 400,
		"completion_reason": "end_turn",
		"started_at": "2026-06-02T00:00:00Z",
		"ended_at":   "2026-06-02T00:00:01Z",
		"intent": "log_nutrition"
	}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/turns", body)
	w := httptest.NewRecorder()
	h.turn(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	if len(sink.calls) != 1 || sink.calls[0].intent != "log_nutrition" {
		t.Fatalf("sink: %+v", sink.calls)
	}
}

func TestTurnHandler_SkipsLastIntentWriteForGeneral(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()
	sink := &fakeIntentSink{}
	h := NewHandlerWithIntentSink(repo, sink)

	body := strings.NewReader(`{
		"id": "turn-2",
		"user_id": "u-1",
		"session_id": "11111111-2222-4333-8444-555555555555",
		"model": "claude-haiku-4-5-20251001",
		"routed_tier": "simple",
		"router_model": "claude-haiku-4-5-20251001",
		"router_latency_ms": 400,
		"completion_reason": "end_turn",
		"started_at": "2026-06-02T00:00:00Z",
		"ended_at":   "2026-06-02T00:00:01Z",
		"intent": "general"
	}`)
	req := httptest.NewRequest("POST", "/internal/telemetry/turns", body)
	w := httptest.NewRecorder()
	h.turn(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d", w.Code)
	}
	if len(sink.calls) != 0 {
		t.Fatalf("sink should be empty, got %+v", sink.calls)
	}
}
