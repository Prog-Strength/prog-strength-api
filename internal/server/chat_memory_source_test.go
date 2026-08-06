package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/chat"
	"github.com/Prog-Strength/prog-strength-api/internal/db"
)

// openChatMemoryTestDB returns a fully-migrated temp app.db for the chat source
// tests.
func openChatMemoryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// seedChatSession inserts a chat_sessions row with an explicit last_message_at
// and no messages — the shape the clients create just before streaming the
// first turn.
func seedChatSession(t *testing.T, d *sql.DB, id, userID string, lastMsg time.Time) {
	t.Helper()
	mustExec(t, d, `INSERT INTO chat_sessions (id, user_id, title, created_at, updated_at, last_message_at)
		VALUES (?, ?, '', ?, ?, ?)`, id, userID, lastMsg, lastMsg, lastMsg)
}

// seedChatMessage appends one message to a seeded session.
func seedChatMessage(t *testing.T, d *sql.DB, sessionID, role, content string, position int, at time.Time) {
	t.Helper()
	mustExec(t, d, `INSERT INTO chat_messages (session_id, position, role, content, created_at)
		VALUES (?, ?, ?, ?, ?)`, sessionID, position, role, content, at)
}

// TestChatMemorySource_PendingUnitsNeverBlank is the end-to-end guard for issue
// #78: a session whose first turn never persisted must not reach the distiller,
// because the empty transcript it renders is rejected by the provider (400
// "messages.0: user messages must have non-empty content") and the job's
// mark-on-success-only policy then retries it on every tick forever.
func TestChatMemorySource_PendingUnitsNeverBlank(t *testing.T) {
	ctx := context.Background()
	d := openChatMemoryTestDB(t)

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	idle := now.Add(-2 * time.Hour)

	// The abandoned session: row created, first turn never persisted.
	seedChatSession(t, d, "sess-empty", "u1", idle)
	// A real conversation.
	seedChatSession(t, d, "sess-real", "u1", idle)
	seedChatMessage(t, d, "sess-real", "user", "I'm traveling for work all next week", 1, idle)
	seedChatMessage(t, d, "sess-real", "assistant", "Noted — I'll plan hotel-gym sessions.", 2, idle)

	src := &chatMemorySource{
		chat:       chat.NewSQLiteRepository(d),
		idleWindow: 30 * time.Minute,
	}

	units, err := src.PendingUnits(ctx, now, 10)
	if err != nil {
		t.Fatalf("PendingUnits: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("expected only the session with a transcript, got %d units: %+v", len(units), units)
	}
	if units[0].UnitID != "sess-real" {
		t.Fatalf("selected unit = %q, want sess-real", units[0].UnitID)
	}
	for _, u := range units {
		if strings.TrimSpace(u.Content) == "" {
			t.Fatalf("unit %q has blank content — the distiller would 400 on it", u.UnitID)
		}
	}

	// The backlog gauge must agree with what the job can actually process.
	count, err := src.CountPending(ctx, now)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountPending = %d, want 1", count)
	}

	// The backfill drains the same set without the settle window.
	all, _, err := src.AllUndistilled(ctx, "", 10)
	if err != nil {
		t.Fatalf("AllUndistilled: %v", err)
	}
	if len(all) != 1 || all[0].UnitID != "sess-real" {
		t.Fatalf("expected only sess-real from AllUndistilled, got %+v", all)
	}
}

// TestChatMemorySource_EmptySessionBecomesEligible pins that excluding a blank
// session only defers it: the moment a real turn lands, it is selected normally.
// This is why the fix filters at selection rather than stamping blank sessions
// distilled — stamping would permanently exclude a session the user later uses,
// since AppendTurn never clears memory_distilled_at.
func TestChatMemorySource_EmptySessionBecomesEligible(t *testing.T) {
	ctx := context.Background()
	d := openChatMemoryTestDB(t)

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	idle := now.Add(-2 * time.Hour)

	seedChatSession(t, d, "sess-later", "u1", idle)

	src := &chatMemorySource{
		chat:       chat.NewSQLiteRepository(d),
		idleWindow: 30 * time.Minute,
	}

	units, err := src.PendingUnits(ctx, now, 10)
	if err != nil {
		t.Fatalf("PendingUnits before: %v", err)
	}
	if len(units) != 0 {
		t.Fatalf("expected the blank session to be skipped, got %+v", units)
	}

	// The user comes back to that same session and actually talks.
	seedChatMessage(t, d, "sess-later", "user", "my left shoulder is cranky again", 1, idle)

	units, err = src.PendingUnits(ctx, now, 10)
	if err != nil {
		t.Fatalf("PendingUnits after: %v", err)
	}
	if len(units) != 1 || units[0].UnitID != "sess-later" {
		t.Fatalf("expected the now-populated session to be selected, got %+v", units)
	}
	if !strings.Contains(units[0].Content, "left shoulder is cranky") {
		t.Fatalf("transcript not assembled: %q", units[0].Content)
	}
}
