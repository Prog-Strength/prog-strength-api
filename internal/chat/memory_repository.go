package chat

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Compile-time assertion that *MemoryRepository satisfies Repository.
// Same convention every domain repo follows.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is a thread-safe in-memory implementation. Used
// by the handler tests and as a fallback for local development
// against the in-memory wiring.
type MemoryRepository struct {
	mu       sync.Mutex
	sessions map[string]*Session
	// messages is keyed by session id. Each value is the ordered
	// slice of that session's messages. AppendTurn pushes to the
	// tail; ListMessages returns a defensive copy.
	messages map[string][]Message
	now      func() time.Time
	// nextMsgID hands out monotonically increasing message ids so
	// the in-memory shape mirrors the SQLite AUTOINCREMENT column.
	nextMsgID int64
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		sessions: make(map[string]*Session),
		messages: make(map[string][]Message),
		now:      time.Now,
	}
}

func (r *MemoryRepository) CreateSession(ctx context.Context, s *Session) error {
	if err := s.ValidateForCreate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.sessions[s.ID]; exists {
		return ErrSessionIDExists
	}

	// Eager eviction: count the user's active sessions; if at or above
	// the cap, hard-delete the oldest before inserting the new one.
	active := r.activeForUserLocked(s.UserID)
	if len(active) >= MaxSessionsPerUser {
		sort.Slice(active, func(i, j int) bool {
			return active[i].LastMessageAt.Before(active[j].LastMessageAt)
		})
		evicted := active[0]
		delete(r.sessions, evicted.ID)
		delete(r.messages, evicted.ID) // CASCADE equivalent
	}

	now := r.now().UTC()
	stored := *s
	stored.CreatedAt = now
	stored.UpdatedAt = now
	stored.LastMessageAt = now
	stored.DeletedAt = nil
	r.sessions[stored.ID] = &stored

	// Reflect the persisted state back to the caller.
	*s = stored
	return nil
}

func (r *MemoryRepository) GetSession(ctx context.Context, userID, sessionID string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok || s.DeletedAt != nil || s.UserID != userID {
		return nil, ErrNotFound
	}
	clone := *s
	return &clone, nil
}

func (r *MemoryRepository) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.activeForUserLocked(userID)
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastMessageAt.After(out[j].LastMessageAt)
	})
	return out, nil
}

func (r *MemoryRepository) SetTitle(ctx context.Context, userID, sessionID, title string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok || s.DeletedAt != nil || s.UserID != userID {
		return ErrNotFound
	}
	s.Title = title
	s.UpdatedAt = r.now().UTC()
	return nil
}

func (r *MemoryRepository) SoftDeleteSession(ctx context.Context, userID, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok || s.DeletedAt != nil || s.UserID != userID {
		return ErrNotFound
	}
	now := r.now().UTC()
	s.DeletedAt = &now
	s.UpdatedAt = now
	return nil
}

func (r *MemoryRepository) AppendTurn(ctx context.Context, userID, sessionID string, turn Turn) (Session, []Message, error) {
	if err := turn.ValidateForAppend(); err != nil {
		return Session{}, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok || s.DeletedAt != nil || s.UserID != userID {
		return Session{}, nil, ErrNotFound
	}
	existing := r.messages[sessionID]
	basePos := len(existing) // 0 for an empty session

	now := r.now().UTC()
	r.nextMsgID++
	userMsg := Message{
		ID:        r.nextMsgID,
		SessionID: sessionID,
		Position:  basePos,
		Role:      RoleUser,
		Content:   turn.User.Content,
		CreatedAt: now,
	}
	r.nextMsgID++
	assistantMsg := Message{
		ID:        r.nextMsgID,
		SessionID: sessionID,
		Position:  basePos + 1,
		Role:      RoleAssistant,
		Content:   turn.Assistant.Content,
		Model:     cloneStringPtr(turn.Assistant.Model),
		ToolsJSON: cloneStringPtr(turn.Assistant.ToolsJSON),
		CreatedAt: now,
	}
	r.messages[sessionID] = append(existing, userMsg, assistantMsg)
	s.LastMessageAt = now
	s.UpdatedAt = now

	return *s, []Message{userMsg, assistantMsg}, nil
}

func (r *MemoryRepository) ListMessages(ctx context.Context, userID, sessionID string) ([]Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok || s.DeletedAt != nil || s.UserID != userID {
		return nil, ErrNotFound
	}
	msgs := r.messages[sessionID]
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

// activeForUserLocked returns a value-typed defensive copy of every
// non-deleted session for the user. Caller must hold r.mu.
func (r *MemoryRepository) activeForUserLocked(userID string) []Session {
	var out []Session
	for _, s := range r.sessions {
		if s.UserID == userID && s.DeletedAt == nil {
			out = append(out, *s)
		}
	}
	return out
}

// cloneStringPtr returns a fresh *string whose value matches src.
// Used so AppendTurn's stored rows don't share pointers with the
// caller's input struct — defensive copy discipline per CLAUDE.md.
func cloneStringPtr(src *string) *string {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
