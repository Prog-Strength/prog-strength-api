package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// MemoryRepository tests cover the interface contract — the SQLite
// tests below it cover the same plus the eviction-cascade-via-FK
// behavior the in-memory version simulates manually. Tests are split
// across files so a failure tells you immediately whether the
// contract drifted or the SQL did.

func TestMemoryRepository_CreateSession_RejectsBadInput(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	if err := repo.CreateSession(ctx, &Session{UserID: ""}); !errors.Is(err, ErrSessionIDRequired) && !errors.Is(err, ErrUserIDRequired) {
		t.Errorf("empty user+id: got %v, want a Required error", err)
	}
	if err := repo.CreateSession(ctx, &Session{ID: "not-a-uuid", UserID: "u1"}); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("non-uuid: got %v, want ErrInvalidSessionID", err)
	}
}

func TestMemoryRepository_CreateSession_RejectsDuplicateID(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u2"}); !errors.Is(err, ErrSessionIDExists) {
		t.Errorf("duplicate id: got %v, want ErrSessionIDExists", err)
	}
}

func TestMemoryRepository_AppendTurn_AssignsPositions(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	model := "claude-sonnet-4-6"
	_, msgs, err := repo.AppendTurn(ctx, "u1", id, Turn{
		User:      Message{Role: RoleUser, Content: "hi"},
		Assistant: Message{Role: RoleAssistant, Content: "hello", Model: &model},
	})
	if err != nil {
		t.Fatalf("append first turn: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Position != 0 || msgs[1].Position != 1 {
		t.Errorf("first turn positions = [%d,%d], want [0,1]", msgs[0].Position, msgs[1].Position)
	}

	_, msgs, err = repo.AppendTurn(ctx, "u1", id, Turn{
		User:      Message{Role: RoleUser, Content: "what's next?"},
		Assistant: Message{Role: RoleAssistant, Content: "more", Model: &model},
	})
	if err != nil {
		t.Fatalf("append second turn: %v", err)
	}
	if msgs[0].Position != 2 || msgs[1].Position != 3 {
		t.Errorf("second turn positions = [%d,%d], want [2,3]", msgs[0].Position, msgs[1].Position)
	}
}

func TestMemoryRepository_AppendTurn_RejectsEmptyContent(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err := repo.AppendTurn(ctx, "u1", id, Turn{
		User:      Message{Content: ""},
		Assistant: Message{Content: "reply"},
	})
	if !errors.Is(err, ErrEmptyContent) {
		t.Errorf("empty user content: got %v, want ErrEmptyContent", err)
	}
}

func TestMemoryRepository_GetSession_ScopedByUser(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "owner"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := repo.GetSession(ctx, "other", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong-user get: got %v, want ErrNotFound (no info leak)", err)
	}
}

func TestMemoryRepository_SoftDelete_HidesFromReads(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.SoftDeleteSession(ctx, "u1", id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetSession(ctx, "u1", id); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: got %v, want ErrNotFound", err)
	}
	list, err := repo.ListSessions(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list after delete: got %d, want 0", len(list))
	}
}

func TestMemoryRepository_Eviction_DropsOldestOverCap(t *testing.T) {
	// Drive the clock manually so we know exactly which session is the
	// oldest when eviction kicks in. Each create gets a unique
	// last_message_at via the injected now.
	t0 := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	tick := t0
	repo := NewMemoryRepository()
	repo.now = func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	ctx := context.Background()

	ids := make([]string, MaxSessionsPerUser+1)
	for i := 0; i < MaxSessionsPerUser+1; i++ {
		ids[i] = uuid(i + 1)
		if err := repo.CreateSession(ctx, &Session{ID: ids[i], UserID: "u1"}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// The first one in (oldest last_message_at) should be gone.
	if _, err := repo.GetSession(ctx, "u1", ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected oldest to be evicted, got %v", err)
	}
	// The most recent should be present.
	if _, err := repo.GetSession(ctx, "u1", ids[len(ids)-1]); err != nil {
		t.Errorf("newest should be present: %v", err)
	}
	// Active count never exceeds the cap.
	list, err := repo.ListSessions(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != MaxSessionsPerUser {
		t.Errorf("active sessions = %d, want %d", len(list), MaxSessionsPerUser)
	}
}

func TestMemoryRepository_SetTitle_BumpsUpdatedAt(t *testing.T) {
	t0 := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	tick := t0
	repo := NewMemoryRepository()
	repo.now = func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	ctx := context.Background()
	id := uuid(1)
	if err := repo.CreateSession(ctx, &Session{ID: id, UserID: "u1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := repo.GetSession(ctx, "u1", id)
	if err != nil {
		t.Fatalf("get pre-title: %v", err)
	}
	if err = repo.SetTitle(ctx, "u1", id, "My Workouts"); err != nil {
		t.Fatalf("set title: %v", err)
	}
	after, err := repo.GetSession(ctx, "u1", id)
	if err != nil {
		t.Fatalf("get post-title: %v", err)
	}
	if after.Title != "My Workouts" {
		t.Errorf("title = %q, want %q", after.Title, "My Workouts")
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at didn't bump: before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestMemoryRepository_SessionIntentRoundTrip(t *testing.T) {
	repo := NewMemoryRepository()
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

func TestMemoryRepository_GetSessionIntent_UnknownSession(t *testing.T) {
	repo := NewMemoryRepository()
	_, _, err := repo.GetSessionIntent(context.Background(), "11111111-2222-4333-8444-555555555555")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// uuid is a quick "give me a deterministic uuid-shaped string" helper.
// The repos only check format, not version bits — any 8-4-4-4-12 hex
// string passes. Fixture readability beats correct-version v4 noise.
func uuid(n int) string {
	s := pad(n*1111111111, 32) // up to 32 hex chars
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

func pad(n int, w int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, w)
	for i := range out {
		out[i] = '0'
	}
	i := w
	for n > 0 && i > 0 {
		i--
		out[i] = hex[n&0xf]
		n >>= 4
	}
	return strings.ToLower(string(out))
}
