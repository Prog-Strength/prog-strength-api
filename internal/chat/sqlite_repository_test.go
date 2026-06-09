package chat

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// SQLite tests exercise the same contract the in-memory tests do,
// plus two SQL-specific behaviors: the CASCADE on chat_messages.
// session_id when an eviction hard-deletes its parent, and the
// position auto-assignment by COALESCE(MAX(position), -1) + 1
// pattern (in-memory derives positions from slice length).

func TestSQLite_AppendThenList_RoundTrip(t *testing.T) {
	repo, _ := newSQLiteRepo(t)
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	model := "claude-haiku-4-5"
	tools := `[{"name":"list_workouts","state":"ok"}]`
	_, msgs, err := repo.AppendTurn(ctx, "u1", id, Turn{
		User:      Message{Content: "how am I doing?"},
		Assistant: Message{Content: "great", Model: &model, ToolsJSON: &tools},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Position != 0 || msgs[1].Position != 1 {
		t.Errorf("append positions = [%d,%d], want [0,1]", msgs[0].Position, msgs[1].Position)
	}

	got, err := repo.ListMessages(ctx, "u1", id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("list len = %d, want 2", len(got))
	}
	if got[0].Role != RoleUser || got[1].Role != RoleAssistant {
		t.Errorf("roles = [%s,%s], want [user,assistant]", got[0].Role, got[1].Role)
	}
	if got[1].Model == nil || *got[1].Model != model {
		t.Errorf("assistant model didn't round-trip: %+v", got[1].Model)
	}
	if got[1].ToolsJSON == nil || *got[1].ToolsJSON != tools {
		t.Errorf("assistant tools_json didn't round-trip: %+v", got[1].ToolsJSON)
	}
}

func TestSQLite_Eviction_CascadeDeletesMessages(t *testing.T) {
	// Drive the clock so we know which session is oldest. Same shape
	// as the in-memory eviction test; the SQL-specific assertion is
	// that the evicted session's messages are gone from chat_messages
	// — proves the ON DELETE CASCADE FK actually fired.
	t0 := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	tick := t0
	repo, sqlDB := newSQLiteRepo(t)
	repo.now = func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	ctx := context.Background()

	oldestID := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: oldestID, UserID: "u1"}); err != nil {
		t.Fatalf("create oldest: %v", err)
	}
	if _, _, err := repo.AppendTurn(ctx, "u1", oldestID, Turn{
		User:      Message{Content: "a"},
		Assistant: Message{Content: "b"},
	}); err != nil {
		t.Fatalf("append to oldest: %v", err)
	}

	// Sanity: messages for oldest are present.
	if got, err := repo.ListMessages(ctx, "u1", oldestID); err != nil || len(got) != 2 {
		t.Fatalf("pre-evict list: %d msgs (err=%v), want 2", len(got), err)
	}

	// Fill to cap so the next create evicts oldest.
	for i := 2; i <= MaxSessionsPerUser+1; i++ {
		if err := repo.CreateSession(ctx, &Session{ID: uuid(i), UserID: "u1"}); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}

	// Oldest should be gone from chat_sessions...
	if _, err := repo.GetSession(ctx, "u1", oldestID); !errors.Is(err, ErrNotFound) {
		t.Errorf("oldest should be evicted, got %v", err)
	}
	// ...and its messages should be gone from chat_messages thanks to
	// the FK's ON DELETE CASCADE.
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE session_id = ?`, oldestID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 0 {
		t.Errorf("evicted session left %d orphan messages; CASCADE didn't fire", count)
	}
}

func TestSQLite_SoftDelete_PreservesMessages(t *testing.T) {
	// Counterpart to the eviction test: a user-initiated soft delete
	// should NOT cascade messages — the row hangs around invisible so
	// a future restore-from-trash UI can flip it back.
	repo, sqlDB := newSQLiteRepo(t)
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := repo.AppendTurn(ctx, "u1", id, Turn{
		User:      Message{Content: "a"},
		Assistant: Message{Content: "b"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := repo.SoftDeleteSession(ctx, "u1", id); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	var msgCount int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE session_id = ?`, id).Scan(&msgCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if msgCount != 2 {
		t.Errorf("soft delete dropped messages: have %d, want 2", msgCount)
	}
}

func TestSQLite_ListSessions_OrderedByLastMessageDesc(t *testing.T) {
	t0 := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	tick := t0
	repo, _ := newSQLiteRepo(t)
	repo.now = func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	ctx := context.Background()

	a, b, c := uuid(1), uuid(2), uuid(3)
	for _, id := range []string{a, b, c} {
		if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Touch `b` last so it should bubble to the top of the list.
	if _, _, err := repo.AppendTurn(ctx, "u1", b, Turn{
		User: Message{Content: "x"}, Assistant: Message{Content: "y"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	list, err := repo.ListSessions(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 || list[0].ID != b {
		ids := make([]string, len(list))
		for i, s := range list {
			ids[i] = s.ID
		}
		t.Errorf("list order = %v, want %s first", ids, b)
	}
}

func TestSQLite_SessionIntentRoundTrip(t *testing.T) {
	repo, _ := newSQLiteRepo(t)
	ctx := context.Background()
	s := &Session{ID: "11111111-2222-4333-8444-555555555555", UserID: "u-1"}
	if err := repo.CreateSession(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	gotIntent, gotAt, err := repo.GetSessionIntent(ctx, s.ID)
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if gotIntent != nil || gotAt != nil {
		t.Fatalf("expected nil intent on fresh session, got %v / %v", gotIntent, gotAt)
	}

	when := time.Now().UTC().Truncate(time.Second)
	if err = repo.SetSessionIntent(ctx, s.ID, "log_nutrition", when); err != nil {
		t.Fatalf("set: %v", err)
	}

	gotIntent, gotAt, err = repo.GetSessionIntent(ctx, s.ID)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if gotIntent == nil || *gotIntent != "log_nutrition" || gotAt == nil || !gotAt.Equal(when) {
		t.Fatalf("intent round-trip mismatch: %v / %v", gotIntent, gotAt)
	}
}

func TestSQLite_GetSessionIntent_UnknownSession(t *testing.T) {
	repo, _ := newSQLiteRepo(t)
	_, _, err := repo.GetSessionIntent(context.Background(), "11111111-2222-4333-8444-555555555555")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// --- helpers ------------------------------------------------------

func newSQLiteRepo(t *testing.T) (*SQLiteRepository, *sql.DB) {
	t.Helper()
	// db.Open appends "?_foreign_keys=on&_journal_mode=WAL" itself, so
	// pass a bare file path — the workout tests had to learn the same
	// thing.
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "chat.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewSQLiteRepository(sqlDB), sqlDB
}
