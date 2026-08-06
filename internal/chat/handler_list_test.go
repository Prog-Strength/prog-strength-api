package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// vanishingRepo makes a session disappear the instant the list read
// returns, which is exactly what a concurrent DELETE /chat-sessions/{id}
// (or a CreateSession eviction) does to an in-flight GET /chat-sessions.
// Wrapping the list method rather than sleeping keeps the interleaving
// deterministic.
type vanishingRepo struct {
	Repository
	afterList func()
}

func (v *vanishingRepo) ListSessions(ctx context.Context, userID string) ([]SessionSummary, error) {
	out, err := v.Repository.ListSessions(ctx, userID)
	if v.afterList != nil {
		v.afterList()
	}
	return out, err
}

// TestList_SessionDeletedMidRequest_Does_Not_500 pins issue #77: a session
// that leaves the user's active set while GET /chat-sessions is running is
// a benign race, not a server fault. The list read has to be self-contained
// so no per-row follow-up can come back ErrNotFound and take the whole
// response down with a 500.
func TestList_SessionDeletedMidRequest_Does_Not_500(t *testing.T) {
	base := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()
	const userID = "u1"

	keep, doomed := uuid(1), uuid(2)
	for _, id := range []string{keep, doomed} {
		if err := base.CreateSession(ctx, &Session{ID: id, UserID: userID}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if _, _, err := base.AppendTurn(ctx, userID, id, Turn{
			User:      Message{Content: "hi"},
			Assistant: Message{Content: "hello"},
		}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}

	repo := &vanishingRepo{Repository: base, afterList: func() {
		if err := base.SoftDeleteSession(ctx, userID, doomed); err != nil {
			t.Errorf("soft delete: %v", err)
		}
	}}

	r := chi.NewRouter()
	NewHandler(repo).Mount(r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/chat-sessions", nil)
	req = req.WithContext(auth.WithUserID(req.Context(), userID))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

// TestSQLite_ListSessions_CountsMessagesInOneStatement covers the count
// denorm the SOW specifies: computed alongside the session rows, zero for
// a session with no messages, and blind to other users' rows.
func TestSQLite_ListSessions_CountsMessagesInOneStatement(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()

	chatty, empty, other := uuid(1), uuid(2), uuid(3)
	if err := repo.CreateSession(ctx, &Session{ID: chatty, UserID: "u1"}); err != nil {
		t.Fatalf("create chatty: %v", err)
	}
	if err := repo.CreateSession(ctx, &Session{ID: empty, UserID: "u1"}); err != nil {
		t.Fatalf("create empty: %v", err)
	}
	if err := repo.CreateSession(ctx, &Session{ID: other, UserID: "u2"}); err != nil {
		t.Fatalf("create other: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := repo.AppendTurn(ctx, "u1", chatty, Turn{
			User:      Message{Content: "hi"},
			Assistant: Message{Content: "hello"},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if _, _, err := repo.AppendTurn(ctx, "u2", other, Turn{
		User:      Message{Content: "hi"},
		Assistant: Message{Content: "hello"},
	}); err != nil {
		t.Fatalf("append other: %v", err)
	}

	got, err := repo.ListSessions(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	counts := map[string]int{}
	for _, s := range got {
		counts[s.ID] = s.MessageCount
	}
	if len(counts) != 2 {
		t.Fatalf("listed %d sessions, want 2: %+v", len(counts), counts)
	}
	if counts[chatty] != 4 {
		t.Errorf("chatty count = %d, want 4", counts[chatty])
	}
	if counts[empty] != 0 {
		t.Errorf("empty count = %d, want 0", counts[empty])
	}
}

// TestList_ResponseCarriesMessageCount guards the wire shape the web +
// mobile history panes read.
func TestList_ResponseCarriesMessageCount(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := repo.AppendTurn(ctx, "u1", id, Turn{
		User:      Message{Content: "hi"},
		Assistant: Message{Content: "hello"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	r := chi.NewRouter()
	NewHandler(repo).Mount(r)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/chat-sessions", nil)
	req = req.WithContext(auth.WithUserID(req.Context(), "u1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data []sessionListItem `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].MessageCount != 2 {
		t.Errorf("message_count = %d, want 2", resp.Data[0].MessageCount)
	}
	if resp.Data[0].ID != id {
		t.Errorf("id = %q, want %q", resp.Data[0].ID, id)
	}
}
