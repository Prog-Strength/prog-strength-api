package main

import (
	"context"
	"database/sql"
	"io"
	"log"
	"strconv"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// fakeDistiller returns a fixed observation set per conversation index and
// records the prompt hint it was called with so a test can assert each source's
// PromptHint is forwarded. Because the backfill calls DistillBatch once per
// source page, calls accumulate (one entry per page).
type fakeDistiller struct {
	out   [][]string
	seen  int
	hints []string
}

func (f *fakeDistiller) DistillBatch(_ context.Context, conversations []string, promptHint string) ([][]string, error) {
	f.seen += len(conversations)
	f.hints = append(f.hints, promptHint)
	// The backfill flattens per-page; return one observation set per
	// conversation in THIS page. f.out is indexed by the call's conversations.
	out := make([][]string, len(conversations))
	for i := range conversations {
		if i < len(f.out) {
			out[i] = f.out[i]
		}
	}
	return out, nil
}

// fakeEmbedder returns a 1536-dim vector for every input (value = index+1 at
// position 0) so each observation gets a distinct, valid embedding.
type fakeEmbedder struct {
	seen int
}

func (f *fakeEmbedder) EmbedBatch(_ context.Context, inputs []string) ([][]float32, error) {
	f.seen += len(inputs)
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, 1536)
		v[0] = float32(i + 1)
		out[i] = v
	}
	return out, nil
}

// fakeSource implements vectormemory.MemorySource for the backfill. It yields
// canned DistillUnits across MULTIPLE pages (pageSize at a time) so the
// pagination drain is exercised, forwards a settable SourceType/PromptHint and
// per-unit provenance, and records every MarkDistilled call.
type fakeSource struct {
	sourceType string
	units      []vectormemory.DistillUnit
	pageSize   int

	marked []string // unit ids passed to MarkDistilled, in order
}

func (s *fakeSource) SourceType() string { return s.sourceType }

func (s *fakeSource) PendingUnits(context.Context, time.Time, int) ([]vectormemory.DistillUnit, error) {
	return nil, nil // unused by backfill
}

func (s *fakeSource) CountPending(context.Context, time.Time) (int, error) {
	return 0, nil // unused by backfill
}

// AllUndistilled returns one page of units starting at the integer offset
// encoded in cursor ("" == 0). The returned next cursor is the new offset, or
// "" once the slice is exhausted.
func (s *fakeSource) AllUndistilled(_ context.Context, cursor string, limit int) ([]vectormemory.DistillUnit, string, error) {
	off := 0
	if cursor != "" {
		var err error
		off, err = strconv.Atoi(cursor)
		if err != nil {
			return nil, "", err
		}
	}
	page := s.pageSize
	if page == 0 || page > limit {
		page = limit
	}
	end := off + page
	if end > len(s.units) {
		end = len(s.units)
	}
	if off >= len(s.units) {
		return nil, "", nil
	}
	next := ""
	if end < len(s.units) {
		next = strconv.Itoa(end)
	}
	return s.units[off:end], next, nil
}

func (s *fakeSource) MarkDistilled(_ context.Context, unitID string, _ time.Time) error {
	s.marked = append(s.marked, unitID)
	return nil
}

func newDeps(dist batchDistiller, emb batchEmbedder, repo memoryRepo, sources []vectormemory.MemorySource) backfillDeps {
	return backfillDeps{
		cfg: config.VectorMemoryConfig{
			EmbedModel: "text-embedding-3-small",
			EmbedDim:   1536,
		},
		distiller: dist,
		embedder:  emb,
		repo:      repo,
		sources:   sources,
		logger:    log.New(io.Discard, "", 0),
		now:       time.Now,
	}
}

// seedSession / seedWorkout insert the parent rows the agent_memories FK +
// CHECK constraints require so a provenance-carrying insert succeeds.
func seedSession(t *testing.T, database *sql.DB, id, userID string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO chat_sessions (id, user_id, title, created_at, updated_at, last_message_at)
		VALUES (?, ?, '', ?, ?, ?)
	`, id, userID, now, now, now); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

func seedWorkout(t *testing.T, database *sql.DB, id, userID string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO workouts (id, user_id, name, performed_at, notes, created_at, updated_at)
		VALUES (?, ?, 'leg day', ?, 'felt strong', ?, ?)
	`, id, userID, now, now, now); err != nil {
		t.Fatalf("seed workout %s: %v", id, err)
	}
}

func countMemories(t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT count(*) FROM agent_memories`).Scan(&n); err != nil {
		t.Fatalf("count agent_memories: %v", err)
	}
	return n
}

func chatUnit(sessionID, userID, content string) vectormemory.DistillUnit {
	sid := sessionID
	return vectormemory.DistillUnit{
		UnitID:     sessionID,
		UserID:     userID,
		Content:    content,
		PromptHint: "",
		Source:     vectormemory.Provenance{SourceType: "chat_session", SessionID: &sid},
	}
}

func workoutUnit(workoutID, userID, content, hint string) vectormemory.DistillUnit {
	wid := workoutID
	return vectormemory.DistillUnit{
		UnitID:     workoutID,
		UserID:     userID,
		Content:    content,
		PromptHint: hint,
		Source:     vectormemory.Provenance{SourceType: "workout_note", WorkoutID: &wid},
	}
}

// TestBackfillEndToEnd drives TWO fake sources (chat + workout) through
// multi-page AllUndistilled drains and asserts: both sources fully drained,
// each source's PromptHint forwarded to DistillBatch, inserts carry the right
// provenance per source, and every processed unit is MarkDistilled.
func TestBackfillEndToEnd(t *testing.T) {
	database := dbtest.New(t)
	seedSession(t, database, "s1", "u1")
	seedSession(t, database, "s2", "u1")
	seedSession(t, database, "s3", "u2")
	seedWorkout(t, database, "w1", "u1")
	seedWorkout(t, database, "w2", "u2")

	const workoutHint = "extract durable workout facts"

	chatSrc := &fakeSource{
		sourceType: "chat_session",
		pageSize:   2, // 3 units → 2 pages (2 + 1)
		units: []vectormemory.DistillUnit{
			chatUnit("s1", "u1", "I travel for work."),
			chatUnit("s2", "u1", "Cutting for a meet."),
			chatUnit("s3", "u2", "Trains in hotel gyms."),
		},
	}
	workoutSrc := &fakeSource{
		sourceType: "workout_note",
		pageSize:   1, // 2 units → 2 pages (1 + 1)
		units: []vectormemory.DistillUnit{
			workoutUnit("w1", "u1", "Workout notes: left shoulder cranky", workoutHint),
			workoutUnit("w2", "u2", "Workout notes: hotel gym only", workoutHint),
		},
	}

	// One observation per conversation in each page (fakeDistiller indexes f.out
	// by the page's conversation index, so a single-element out works per page).
	dist := &fakeDistiller{out: [][]string{{"obs"}, {"obs"}}}
	emb := &fakeEmbedder{}
	repo := vectormemory.NewSQLiteRepository(database)

	deps := newDeps(dist, emb, repo, []vectormemory.MemorySource{chatSrc, workoutSrc})
	if err := backfill(context.Background(), deps, false); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Both sources fully drained across pages.
	if len(chatSrc.marked) != 3 {
		t.Errorf("chat marked %v, want 3 units", chatSrc.marked)
	}
	if len(workoutSrc.marked) != 2 {
		t.Errorf("workout marked %v, want 2 units", workoutSrc.marked)
	}

	// 5 units total → 5 observations → 5 inserts.
	if got := countMemories(t, database); got != 5 {
		t.Errorf("inserted %d memories, want 5", got)
	}
	if dist.seen != 5 {
		t.Errorf("distiller saw %d conversations, want 5", dist.seen)
	}
	if emb.seen != 5 {
		t.Errorf("embedder saw %d observations, want 5", emb.seen)
	}

	// Each source's hint forwarded. Chat pages carry "", workout pages carry the
	// workout hint. 2 chat pages + 2 workout pages = 4 DistillBatch calls.
	var chatHints, workoutHints int
	for _, h := range dist.hints {
		switch h {
		case "":
			chatHints++
		case workoutHint:
			workoutHints++
		default:
			t.Errorf("unexpected prompt hint %q", h)
		}
	}
	if chatHints != 2 {
		t.Errorf("got %d empty (chat) hints, want 2", chatHints)
	}
	if workoutHints != 2 {
		t.Errorf("got %d workout hints, want 2", workoutHints)
	}

	// Provenance: chat units land on source_session_id, workout units on
	// source_workout_id (the CHECK constraint already guarantees mutual
	// exclusion; this confirms the discriminator routing).
	var chatRows, workoutRows int
	if err := database.QueryRow(`SELECT count(*) FROM agent_memories WHERE source_type='chat_session' AND source_session_id IS NOT NULL AND source_workout_id IS NULL`).Scan(&chatRows); err != nil {
		t.Fatalf("count chat provenance: %v", err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM agent_memories WHERE source_type='workout_note' AND source_workout_id IS NOT NULL AND source_session_id IS NULL`).Scan(&workoutRows); err != nil {
		t.Fatalf("count workout provenance: %v", err)
	}
	if chatRows != 3 {
		t.Errorf("chat-provenance rows = %d, want 3", chatRows)
	}
	if workoutRows != 2 {
		t.Errorf("workout-provenance rows = %d, want 2", workoutRows)
	}
}

// TestBackfillIdempotent verifies a re-run whose sources return nothing from
// AllUndistilled is a no-op: no distillation, no inserts.
func TestBackfillIdempotent(t *testing.T) {
	database := dbtest.New(t)

	// Empty sources stand in for an already-fully-distilled corpus (every unit
	// already stamped, so AllUndistilled yields nothing).
	chatSrc := &fakeSource{sourceType: "chat_session", pageSize: 2}
	workoutSrc := &fakeSource{sourceType: "workout_note", pageSize: 2}

	dist := &fakeDistiller{out: nil}
	emb := &fakeEmbedder{}
	repo := vectormemory.NewSQLiteRepository(database)

	deps := newDeps(dist, emb, repo, []vectormemory.MemorySource{chatSrc, workoutSrc})
	if err := backfill(context.Background(), deps, false); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if dist.seen != 0 {
		t.Errorf("distiller saw %d conversations, want 0 (nothing to distill)", dist.seen)
	}
	if len(chatSrc.marked)+len(workoutSrc.marked) != 0 {
		t.Errorf("marked units on a no-op run: chat=%v workout=%v", chatSrc.marked, workoutSrc.marked)
	}
	if got := countMemories(t, database); got != 0 {
		t.Errorf("inserted %d memories on no-op, want 0", got)
	}
}

// TestBackfillDryRun verifies dry-run still spends on the batch APIs (to produce
// counts) but writes no memory rows and marks no units.
func TestBackfillDryRun(t *testing.T) {
	database := dbtest.New(t)
	seedSession(t, database, "s1", "u1")
	seedWorkout(t, database, "w1", "u1")

	chatSrc := &fakeSource{
		sourceType: "chat_session",
		pageSize:   2,
		units:      []vectormemory.DistillUnit{chatUnit("s1", "u1", "I travel for work.")},
	}
	workoutSrc := &fakeSource{
		sourceType: "workout_note",
		pageSize:   2,
		units:      []vectormemory.DistillUnit{workoutUnit("w1", "u1", "Workout notes: cranky", "hint")},
	}

	dist := &fakeDistiller{out: [][]string{{"obs"}}}
	emb := &fakeEmbedder{}
	repo := vectormemory.NewSQLiteRepository(database)

	deps := newDeps(dist, emb, repo, []vectormemory.MemorySource{chatSrc, workoutSrc})
	if err := backfill(context.Background(), deps, true); err != nil {
		t.Fatalf("dry-run backfill: %v", err)
	}

	// Dry-run still distills + embeds (to produce the would-insert counts).
	if dist.seen != 2 {
		t.Errorf("distiller saw %d conversations, want 2 (dry-run still distills)", dist.seen)
	}
	if emb.seen != 2 {
		t.Errorf("embedder saw %d observations, want 2 (dry-run still embeds)", emb.seen)
	}
	// But nothing is written or marked.
	if got := countMemories(t, database); got != 0 {
		t.Errorf("dry-run inserted %d memories, want 0", got)
	}
	if len(chatSrc.marked)+len(workoutSrc.marked) != 0 {
		t.Errorf("dry-run marked units: chat=%v workout=%v", chatSrc.marked, workoutSrc.marked)
	}
}
