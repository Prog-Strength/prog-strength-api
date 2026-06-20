package chat

import (
	"context"
	"testing"
	"time"
)

// seedDistillSession inserts a chat_sessions row with explicit
// last_message_at / memory_distilled_at / deleted_at so the distillation
// selection query can be exercised against known state. memory_distilled_at
// and deleted_at are written as nullable timestamps (nil ⇒ SQL NULL).
func seedDistillSession(t *testing.T, repo *SQLiteRepository, id, userID string, lastMsg time.Time, distilledAt, deletedAt *time.Time) {
	t.Helper()
	_, err := repo.db.ExecContext(context.Background(), `
		INSERT INTO chat_sessions (
			id, user_id, title, created_at, updated_at,
			last_message_at, deleted_at, memory_distilled_at
		) VALUES (?, ?, '', ?, ?, ?, ?, ?)
	`, id, userID, lastMsg.UTC(), lastMsg.UTC(), lastMsg.UTC(), deletedAt, distilledAt)
	if err != nil {
		t.Fatalf("seed distill session %s: %v", id, err)
	}
}

func TestSQLite_IdleUndistilled(t *testing.T) {
	repo, _ := newSQLiteRepo(t)
	ctx := context.Background()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour) // idle
	recent := now.Add(-1 * time.Minute)
	distilled := now.Add(-90 * time.Minute)
	deleted := now.Add(-30 * time.Minute)

	// idle + undistilled + live ⇒ selected (oldest)
	seedDistillSession(t, repo, uuid(1), "u1", old.Add(-time.Hour), nil, nil)
	// idle + undistilled + live ⇒ selected (newer of the two idle)
	seedDistillSession(t, repo, uuid(2), "u2", old, nil, nil)
	// not idle yet ⇒ excluded
	seedDistillSession(t, repo, uuid(3), "u1", recent, nil, nil)
	// already distilled ⇒ excluded
	seedDistillSession(t, repo, uuid(4), "u1", old, &distilled, nil)
	// soft-deleted ⇒ excluded
	seedDistillSession(t, repo, uuid(5), "u3", old, nil, &deleted)

	cutoff := now.Add(-30 * time.Minute)

	got, err := repo.IdleUndistilled(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("IdleUndistilled: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 idle undistilled sessions, got %d: %+v", len(got), got)
	}
	// Oldest-idle first.
	if got[0].ID != uuid(1) || got[1].ID != uuid(2) {
		t.Fatalf("expected order [%s,%s], got [%s,%s]", uuid(1), uuid(2), got[0].ID, got[1].ID)
	}
	if got[0].UserID != "u1" || got[1].UserID != "u2" {
		t.Fatalf("user ids didn't round-trip: %+v", got)
	}

	t.Run("respects limit", func(t *testing.T) {
		limited, err := repo.IdleUndistilled(ctx, cutoff, 1)
		if err != nil {
			t.Fatalf("IdleUndistilled limit: %v", err)
		}
		if len(limited) != 1 || limited[0].ID != uuid(1) {
			t.Fatalf("expected just oldest %s under limit 1, got %+v", uuid(1), limited)
		}
	})
}

func TestSQLite_SessionMessages_NotUserScoped(t *testing.T) {
	repo, _ := newSQLiteRepo(t)
	ctx := context.Background()

	id := uuid(7)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "owner"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	model := "claude-haiku-4-5"
	if _, _, err := repo.AppendTurn(ctx, "owner", id, Turn{
		User:      Message{Content: "first?"},
		Assistant: Message{Content: "yes", Model: &model},
	}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if _, _, err := repo.AppendTurn(ctx, "owner", id, Turn{
		User:      Message{Content: "second?"},
		Assistant: Message{Content: "also yes", Model: &model},
	}); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	// No user is supplied — the job reads by session id alone.
	got, err := repo.SessionMessages(ctx, id)
	if err != nil {
		t.Fatalf("SessionMessages: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(got))
	}
	// Position-ascending.
	for i := 1; i < len(got); i++ {
		if got[i-1].Position > got[i].Position {
			t.Fatalf("messages not position-ascending: %+v", got)
		}
	}
	if got[0].Content != "first?" || got[3].Content != "also yes" {
		t.Fatalf("unexpected message order/content: %+v", got)
	}
}

func TestSQLite_MarkDistilled(t *testing.T) {
	repo, _ := newSQLiteRepo(t)
	ctx := context.Background()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	seedDistillSession(t, repo, uuid(1), "u1", old, nil, nil)

	cutoff := now.Add(-30 * time.Minute)

	// Present before marking.
	before, err := repo.IdleUndistilled(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("IdleUndistilled before: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected 1 idle session before marking, got %d", len(before))
	}

	if markErr := repo.MarkDistilled(ctx, uuid(1), now); markErr != nil {
		t.Fatalf("MarkDistilled: %v", markErr)
	}

	// Gone after marking — the IS NULL gate now excludes it.
	after, err := repo.IdleUndistilled(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("IdleUndistilled after: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected 0 idle sessions after marking, got %d: %+v", len(after), after)
	}

	// And the column actually holds a (UTC) timestamp.
	var stamped time.Time
	if err := repo.db.QueryRowContext(ctx,
		`SELECT memory_distilled_at FROM chat_sessions WHERE id = ?`, uuid(1),
	).Scan(&stamped); err != nil {
		t.Fatalf("read memory_distilled_at: %v", err)
	}
	if stamped.IsZero() {
		t.Fatal("memory_distilled_at should be set after MarkDistilled")
	}
}
