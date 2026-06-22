package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/chat"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// chatMemorySource adapts the concrete chat SQLite repository to vectormemory's
// MemorySource. It lives here (not in vectormemory) so vectormemory never
// imports chat — the dependency points inward, consumer-side. idleWindow is the
// chat settle window (cfg.SessionIdleMinutes); each source owns its own window
// so the job no longer computes a cutoff.
type chatMemorySource struct {
	chat       *chat.SQLiteRepository
	idleWindow time.Duration
}

var _ vectormemory.MemorySource = (*chatMemorySource)(nil)

func (s *chatMemorySource) SourceType() string { return "chat_session" }

func (s *chatMemorySource) PendingUnits(ctx context.Context, now time.Time, limit int) ([]vectormemory.DistillUnit, error) {
	cutoff := now.Add(-s.idleWindow)
	rows, err := s.chat.IdleUndistilled(ctx, cutoff, limit)
	if err != nil {
		return nil, err
	}
	units := make([]vectormemory.DistillUnit, 0, len(rows))
	for _, r := range rows {
		unit, err := s.assembleUnit(ctx, r.ID, r.UserID)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	return units, nil
}

func (s *chatMemorySource) CountPending(ctx context.Context, now time.Time) (int, error) {
	return s.chat.CountIdleUndistilled(ctx, now.Add(-s.idleWindow))
}

func (s *chatMemorySource) AllUndistilled(ctx context.Context, cursor string, limit int) ([]vectormemory.DistillUnit, string, error) {
	rows, next, err := s.chat.AllUndistilledSessions(ctx, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	units := make([]vectormemory.DistillUnit, 0, len(rows))
	for _, r := range rows {
		unit, err := s.assembleUnit(ctx, r.ID, r.UserID)
		if err != nil {
			return nil, "", err
		}
		units = append(units, unit)
	}
	return units, next, nil
}

func (s *chatMemorySource) MarkDistilled(ctx context.Context, unitID string, at time.Time) error {
	return s.chat.MarkDistilled(ctx, unitID, at)
}

// assembleUnit loads a session's transcript and renders it into a self-contained
// DistillUnit. PromptHint is empty so chat behavior is byte-for-byte unchanged.
func (s *chatMemorySource) assembleUnit(ctx context.Context, sessionID, userID string) (vectormemory.DistillUnit, error) {
	msgs, err := s.chat.SessionMessages(ctx, sessionID)
	if err != nil {
		return vectormemory.DistillUnit{}, err
	}
	conv := make([]vectormemory.ConversationMessage, len(msgs))
	for i, m := range msgs {
		conv[i] = vectormemory.ConversationMessage{Role: string(m.Role), Content: m.Content}
	}
	sid := sessionID
	return vectormemory.DistillUnit{
		UnitID:     sessionID,
		UserID:     userID,
		Content:    vectormemory.RenderConversation(conv),
		PromptHint: "", // chat behavior is unchanged
		Source:     vectormemory.Provenance{SourceType: "chat_session", SessionID: &sid},
	}, nil
}

// BuildMemorySources constructs the distillation source registry. Order is the
// iteration order of the job and backfill: chat first, workout-note second.
// Lives here so the adapters (which import chat/workout schema) stay out of the
// vectormemory package. The workout source reads app.db directly (db), since a
// workout unit spans workouts, workout_exercises, and exercises.
func BuildMemorySources(db *sql.DB, chatRepo *chat.SQLiteRepository, cfg config.VectorMemoryConfig) []vectormemory.MemorySource {
	return []vectormemory.MemorySource{
		&chatMemorySource{chat: chatRepo, idleWindow: time.Duration(cfg.SessionIdleMinutes) * time.Minute},
		&workoutNoteSource{db: db, settleWindow: time.Duration(cfg.WorkoutSettleMinutes) * time.Minute},
	}
}
