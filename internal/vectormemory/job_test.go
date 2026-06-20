package vectormemory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// fakeSessionSource is an in-memory SessionSource. It returns a fixed set of
// idle sessions, serves per-session conversations from a map, records every
// MarkDistilled call, and can inject a Conversation error for one session.
type fakeSessionSource struct {
	idle          []IdleSession
	conversations map[string][]ConversationMessage
	convErrFor    string // session id whose Conversation() call returns an error

	marked map[string]int // session id ⇒ number of MarkDistilled calls
}

func (f *fakeSessionSource) IdleUndistilled(_ context.Context, _ time.Time, limit int) ([]IdleSession, error) {
	if limit < len(f.idle) {
		return f.idle[:limit], nil
	}
	return f.idle, nil
}

func (f *fakeSessionSource) Conversation(_ context.Context, sessionID string) ([]ConversationMessage, error) {
	if sessionID == f.convErrFor {
		return nil, errors.New("load conversation boom")
	}
	return f.conversations[sessionID], nil
}

func (f *fakeSessionSource) MarkDistilled(_ context.Context, sessionID string, _ time.Time) error {
	if f.marked == nil {
		f.marked = map[string]int{}
	}
	f.marked[sessionID]++
	return nil
}

// distillByConv is a fakeDistiller variant keyed on the rendered conversation
// text: it returns a preset observation list for most conversations but errors
// for one specific rendered transcript, letting a test prove a per-session
// distiller failure is skipped without aborting the batch.
type distillByConv struct {
	observations []string
	errFor       string // rendered conversation that should error
}

func (d *distillByConv) Distill(_ context.Context, conversation string) ([]string, error) {
	if conversation == d.errFor {
		return nil, errors.New("distill boom")
	}
	return d.observations, nil
}

func (d *distillByConv) Configured() bool { return true }

func TestDistillOnce_HappyPath_MarksAndPersists(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	emb := &fakeEmbedder{vectors: map[string][]float32{"likes squats": oneHot(0)}}
	dis := &fakeDistiller{observations: []string{"likes squats"}}
	svc := NewService(repo, emb, dis, baseCfg(), testLogger())

	src := &fakeSessionSource{
		idle: []IdleSession{{ID: "s1", UserID: "userA"}},
		conversations: map[string][]ConversationMessage{
			"s1": {{Role: "user", Content: "I love squats"}},
		},
	}

	if err := svc.distillOnce(ctx, src); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	dumped, err := svc.Dump(ctx, "userA", 10, 0)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(dumped) != 1 {
		t.Fatalf("expected 1 persisted memory, got %d", len(dumped))
	}
	if src.marked["s1"] != 1 {
		t.Fatalf("expected s1 marked distilled once, got %d", src.marked["s1"])
	}
}

func TestDistillOnce_ZeroObservations_StillMarks(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	// Distiller yields nothing — DistillSession returns (0, nil), which is
	// success, so the session must still be marked (re-distilling an empty
	// conversation would just re-spend for the same empty result).
	dis := &fakeDistiller{observations: nil}
	svc := NewService(repo, &fakeEmbedder{}, dis, baseCfg(), testLogger())

	src := &fakeSessionSource{
		idle: []IdleSession{{ID: "s1", UserID: "userA"}},
		conversations: map[string][]ConversationMessage{
			"s1": {{Role: "user", Content: "hi"}},
		},
	}

	if err := svc.distillOnce(ctx, src); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	dumped, err := svc.Dump(ctx, "userA", 10, 0)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(dumped) != 0 {
		t.Fatalf("expected no memories for empty distillation, got %d", len(dumped))
	}
	if src.marked["s1"] != 1 {
		t.Fatalf("zero-observation session should still be marked once, got %d", src.marked["s1"])
	}
}

func TestDistillOnce_DistillerError_SkipsAndContinues(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "sA", "userA")
	seedSession(t, db, "sB", "userB")

	// Session A's transcript errors in the distiller; session B's succeeds.
	convA := []ConversationMessage{{Role: "user", Content: "boom please"}}
	convB := []ConversationMessage{{Role: "user", Content: "fine"}}
	dis := &distillByConv{
		observations: []string{"durable fact"},
		errFor:       renderConversation(convA),
	}
	emb := &fakeEmbedder{vectors: map[string][]float32{"durable fact": oneHot(0)}}
	svc := NewService(repo, emb, dis, baseCfg(), testLogger())

	src := &fakeSessionSource{
		idle: []IdleSession{{ID: "sA", UserID: "userA"}, {ID: "sB", UserID: "userB"}},
		conversations: map[string][]ConversationMessage{
			"sA": convA,
			"sB": convB,
		},
	}

	if err := svc.distillOnce(ctx, src); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	// A failed (left unmarked for retry); B succeeded (marked + persisted).
	if src.marked["sA"] != 0 {
		t.Fatalf("session A should NOT be marked after distiller error, got %d", src.marked["sA"])
	}
	if src.marked["sB"] != 1 {
		t.Fatalf("session B should be marked once, got %d", src.marked["sB"])
	}
	dumped, err := svc.Dump(ctx, "userB", 10, 0)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(dumped) != 1 {
		t.Fatalf("expected B's memory persisted, got %d", len(dumped))
	}
}

func TestDistillOnce_ConversationError_SkipsAndContinues(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "sA", "userA")
	seedSession(t, db, "sB", "userB")

	emb := &fakeEmbedder{vectors: map[string][]float32{"fact": oneHot(0)}}
	dis := &fakeDistiller{observations: []string{"fact"}}
	svc := NewService(repo, emb, dis, baseCfg(), testLogger())

	src := &fakeSessionSource{
		idle:       []IdleSession{{ID: "sA", UserID: "userA"}, {ID: "sB", UserID: "userB"}},
		convErrFor: "sA",
		conversations: map[string][]ConversationMessage{
			"sB": {{Role: "user", Content: "hello"}},
		},
	}

	if err := svc.distillOnce(ctx, src); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	if src.marked["sA"] != 0 {
		t.Fatalf("session A should NOT be marked after conversation-load error, got %d", src.marked["sA"])
	}
	if src.marked["sB"] != 1 {
		t.Fatalf("session B should be marked once after A's load error, got %d", src.marked["sB"])
	}
}
