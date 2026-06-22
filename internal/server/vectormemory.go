package server

import (
	"context"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/chat"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// vmSessionSource adapts the concrete chat SQLite repository to the
// vectormemory distillation job's SessionSource seam. It lives here (not in
// vectormemory) so vectormemory never imports chat — the dependency points
// inward, consumer-side. It translates chat's types to vectormemory's.
type vmSessionSource struct{ chat *chat.SQLiteRepository }

var _ vectormemory.SessionSource = (*vmSessionSource)(nil)

func (s vmSessionSource) IdleUndistilled(ctx context.Context, cutoff time.Time, limit int) ([]vectormemory.IdleSession, error) {
	rows, err := s.chat.IdleUndistilled(ctx, cutoff, limit)
	if err != nil {
		return nil, err
	}
	out := make([]vectormemory.IdleSession, len(rows))
	for i, r := range rows {
		out[i] = vectormemory.IdleSession{ID: r.ID, UserID: r.UserID}
	}
	return out, nil
}

func (s vmSessionSource) CountIdleUndistilled(ctx context.Context, cutoff time.Time) (int, error) {
	return s.chat.CountIdleUndistilled(ctx, cutoff)
}

func (s vmSessionSource) Conversation(ctx context.Context, sessionID string) ([]vectormemory.ConversationMessage, error) {
	msgs, err := s.chat.SessionMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]vectormemory.ConversationMessage, len(msgs))
	for i, m := range msgs {
		out[i] = vectormemory.ConversationMessage{Role: string(m.Role), Content: m.Content}
	}
	return out, nil
}

func (s vmSessionSource) MarkDistilled(ctx context.Context, sessionID string, at time.Time) error {
	return s.chat.MarkDistilled(ctx, sessionID, at)
}
