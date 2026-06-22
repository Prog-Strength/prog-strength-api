package vectormemory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// fakeMemorySource is an in-memory MemorySource. It returns a fixed set of
// pending units, records every MarkDistilled call, and can inject a
// PendingUnits error (simulating a per-source select failure) and a
// CountPending override (so a test can prove the backlog gauge sums the count
// method rather than the capped PendingUnits result).
type fakeMemorySource struct {
	sourceType    string
	pending       []DistillUnit
	pendingErr    error // when set, PendingUnits returns this (per-source select failure)
	countOverride *int  // when set, CountPending returns this backlog
	countErr      error // when set, CountPending returns this (backlog count failure)

	marked map[string]int // unit id ⇒ number of MarkDistilled calls
}

func (f *fakeMemorySource) SourceType() string { return f.sourceType }

func (f *fakeMemorySource) PendingUnits(_ context.Context, _ time.Time, limit int) ([]DistillUnit, error) {
	if f.pendingErr != nil {
		return nil, f.pendingErr
	}
	if limit < len(f.pending) {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}

func (f *fakeMemorySource) CountPending(_ context.Context, _ time.Time) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	if f.countOverride != nil {
		return *f.countOverride, nil
	}
	return len(f.pending), nil
}

func (f *fakeMemorySource) AllUndistilled(_ context.Context, _ string, _ int) ([]DistillUnit, string, error) {
	return nil, "", nil
}

func (f *fakeMemorySource) MarkDistilled(_ context.Context, unitID string, _ time.Time) error {
	if f.marked == nil {
		f.marked = map[string]int{}
	}
	f.marked[unitID]++
	return nil
}

// distillByContent is a Distiller keyed on the unit's content: it returns a
// preset observation list for most content but errors for one specific string,
// letting a test prove a per-unit distiller failure is skipped without aborting
// the rest.
type distillByContent struct {
	observations []string
	errFor       string // content that should error
}

func (d *distillByContent) Distill(_ context.Context, content, _ string) ([]string, DistillUsage, error) {
	if content == d.errFor {
		return nil, DistillUsage{}, errors.New("distill boom")
	}
	return d.observations, DistillUsage{}, nil
}

func (d *distillByContent) Configured() bool { return true }

func TestDistillOnce_RangesOverSources_MarksEach(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")
	seedWorkout(t, db, "w1", "userB")

	emb := &fakeEmbedder{vectors: map[string][]float32{
		"likes squats":  oneHot(0),
		"shoulder hurt": oneHot(1),
	}}

	chatSrc := &fakeMemorySource{
		sourceType: "chat_session",
		pending: []DistillUnit{
			chatUnit("userA", "s1", []ConversationMessage{{Role: "user", Content: "I love squats"}}),
		},
	}
	workoutSrc := &fakeMemorySource{
		sourceType: "workout_note",
		pending: []DistillUnit{
			workoutUnit("userB", "w1", "Workout notes: shoulder hurt", "terse training-log notes"),
		},
	}

	// One distiller for both sources: it yields a single observation matching
	// each unit's expected embedding key, keyed on the unit content.
	svc := NewService(repo, emb, &perContentDistiller{
		byContent: map[string][]string{
			chatSrc.pending[0].Content:    {"likes squats"},
			workoutSrc.pending[0].Content: {"shoulder hurt"},
		},
	}, baseCfg(), testLogger())

	if err := svc.distillOnce(ctx, []MemorySource{chatSrc, workoutSrc}); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	if chatSrc.marked["s1"] != 1 {
		t.Fatalf("chat unit s1 marked %d times, want 1", chatSrc.marked["s1"])
	}
	if workoutSrc.marked["w1"] != 1 {
		t.Fatalf("workout unit w1 marked %d times, want 1", workoutSrc.marked["w1"])
	}

	dumpedA, err := svc.Dump(ctx, "userA", 10, 0)
	if err != nil {
		t.Fatalf("dump userA: %v", err)
	}
	if len(dumpedA) != 1 || dumpedA[0].SourceType != "chat_session" {
		t.Fatalf("expected 1 chat memory for userA, got %+v", dumpedA)
	}
	dumpedB, err := svc.Dump(ctx, "userB", 10, 0)
	if err != nil {
		t.Fatalf("dump userB: %v", err)
	}
	if len(dumpedB) != 1 || dumpedB[0].SourceType != "workout_note" {
		t.Fatalf("expected 1 workout memory for userB, got %+v", dumpedB)
	}
}

func TestDistillOnce_ZeroObservationUnit_StillMarks(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	// Distiller yields nothing — DistillUnit returns (0, nil), which is success,
	// so the unit must still be marked (re-distilling empty content would just
	// re-spend for the same empty result).
	dis := &fakeDistiller{observations: nil}
	svc := NewService(repo, &fakeEmbedder{}, dis, baseCfg(), testLogger())

	src := &fakeMemorySource{
		sourceType: "chat_session",
		pending: []DistillUnit{
			chatUnit("userA", "s1", []ConversationMessage{{Role: "user", Content: "hi"}}),
		},
	}

	if err := svc.distillOnce(ctx, []MemorySource{src}); err != nil {
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
		t.Fatalf("zero-observation unit should still be marked once, got %d", src.marked["s1"])
	}
}

func TestDistillOnce_SourcePendingError_DoesNotBlockOtherSource(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedWorkout(t, db, "w1", "userB")

	emb := &fakeEmbedder{vectors: map[string][]float32{"durable fact": oneHot(0)}}
	svc := NewService(repo, emb, &perContentDistiller{
		byContent: map[string][]string{
			"Workout notes: shoulder hurt": {"durable fact"},
		},
	}, baseCfg(), testLogger())

	// The first source's PendingUnits errors; the second still distills + marks.
	badSrc := &fakeMemorySource{sourceType: "chat_session", pendingErr: errors.New("select boom")}
	goodSrc := &fakeMemorySource{
		sourceType: "workout_note",
		pending: []DistillUnit{
			workoutUnit("userB", "w1", "Workout notes: shoulder hurt", "hint"),
		},
	}

	if err := svc.distillOnce(ctx, []MemorySource{badSrc, goodSrc}); err != nil {
		t.Fatalf("distillOnce should not return on a per-source select error: %v", err)
	}

	if goodSrc.marked["w1"] != 1 {
		t.Fatalf("good source's unit should be marked once despite the other source's select error, got %d", goodSrc.marked["w1"])
	}
	dumped, err := svc.Dump(ctx, "userB", 10, 0)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(dumped) != 1 {
		t.Fatalf("expected the good source's memory persisted, got %d", len(dumped))
	}
}

func TestDistillOnce_PerUnitDistillerError_SkipsAndContinues(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "sA", "userA")
	seedSession(t, db, "sB", "userB")

	convA := []ConversationMessage{{Role: "user", Content: "boom please"}}
	convB := []ConversationMessage{{Role: "user", Content: "fine"}}
	dis := &distillByContent{
		observations: []string{"durable fact"},
		errFor:       RenderConversation(convA),
	}
	emb := &fakeEmbedder{vectors: map[string][]float32{"durable fact": oneHot(0)}}
	svc := NewService(repo, emb, dis, baseCfg(), testLogger())

	src := &fakeMemorySource{
		sourceType: "chat_session",
		pending: []DistillUnit{
			chatUnit("userA", "sA", convA),
			chatUnit("userB", "sB", convB),
		},
	}

	if err := svc.distillOnce(ctx, []MemorySource{src}); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	// A failed (left unmarked for retry); B succeeded (marked + persisted).
	if src.marked["sA"] != 0 {
		t.Fatalf("unit sA should NOT be marked after distiller error, got %d", src.marked["sA"])
	}
	if src.marked["sB"] != 1 {
		t.Fatalf("unit sB should be marked once, got %d", src.marked["sB"])
	}
	dumped, err := svc.Dump(ctx, "userB", 10, 0)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(dumped) != 1 {
		t.Fatalf("expected B's memory persisted, got %d", len(dumped))
	}
}

// perContentDistiller returns a content-keyed observation list, so a test with
// multiple units in one tick can give each a distinct, embeddable observation.
type perContentDistiller struct {
	byContent map[string][]string
}

func (d *perContentDistiller) Distill(_ context.Context, content, _ string) ([]string, DistillUsage, error) {
	return d.byContent[content], DistillUsage{}, nil
}

func (d *perContentDistiller) Configured() bool { return true }
