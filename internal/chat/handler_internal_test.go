package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestGetSessionIntent_Found(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := "11111111-2222-4333-8444-555555555555"
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u-1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	when := time.Now().UTC().Truncate(time.Second)
	if err := repo.SetSessionIntent(ctx, id, "log_nutrition", when); err != nil {
		t.Fatalf("set: %v", err)
	}

	r := chi.NewRouter()
	NewHandler(repo).MountInternal(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/internal/chat-sessions/"+id+"/intent", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Intent   *string    `json:"intent"`
			IntentAt *time.Time `json:"intent_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Intent == nil || *resp.Data.Intent != "log_nutrition" {
		t.Fatalf("intent: got %v", resp.Data.Intent)
	}
	if resp.Data.IntentAt == nil || !resp.Data.IntentAt.Equal(when) {
		t.Fatalf("intent_at: got %v", resp.Data.IntentAt)
	}
}

func TestGetSessionIntent_NoIntentYet(t *testing.T) {
	repo := NewMemoryRepository()
	id := "11111111-2222-4333-8444-555555555555"
	_ = repo.CreateSession(context.Background(), &Session{ID: id, UserID: "u-1"})

	r := chi.NewRouter()
	NewHandler(repo).MountInternal(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/internal/chat-sessions/"+id+"/intent", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"intent":null`) {
		t.Fatalf("expected null intent in body, got %s", w.Body.String())
	}
}

func TestGetSessionIntent_UnknownSession_Returns200WithNulls(t *testing.T) {
	// SOW: return the same shape on 404 to keep the agent client trivial.
	repo := NewMemoryRepository()
	r := chi.NewRouter()
	NewHandler(repo).MountInternal(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/internal/chat-sessions/11111111-2222-4333-8444-555555555555/intent", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"intent":null`) {
		t.Fatalf("expected null intent in body, got %s", w.Body.String())
	}
}
