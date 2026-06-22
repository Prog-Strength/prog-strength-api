package main

import (
	"context"
	"database/sql"
	"io"
	"log"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/chat"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
)

// fakeDistiller returns a fixed observation set per conversation index.
type fakeDistiller struct {
	out  [][]string
	seen int
}

func (f *fakeDistiller) DistillBatch(_ context.Context, conversations []string, _ string) ([][]string, error) {
	f.seen = len(conversations)
	return f.out, nil
}

// fakeEmbedder returns a 1536-dim vector for every input (value = index+1 at
// position 0) so each observation gets a distinct, valid embedding.
type fakeEmbedder struct {
	seen int
}

func (f *fakeEmbedder) EmbedBatch(_ context.Context, inputs []string) ([][]float32, error) {
	f.seen = len(inputs)
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, 1536)
		v[0] = float32(i + 1)
		out[i] = v
	}
	return out, nil
}

// seedSession inserts a chat session + one message so the backfill enumerator
// finds it and SessionMessages returns a transcript.
func seedSession(t *testing.T, database *sql.DB, id, userID string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO chat_sessions (id, user_id, title, created_at, updated_at, last_message_at)
		VALUES (?, ?, '', ?, ?, ?)
	`, id, userID, now, now, now); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
	if _, err := database.Exec(`
		INSERT INTO chat_messages (session_id, position, role, content, created_at)
		VALUES (?, 0, 'user', 'I travel for work and train in hotel gyms.', ?)
	`, id, now); err != nil {
		t.Fatalf("seed message for %s: %v", id, err)
	}
}

func newDeps(t *testing.T, database *sql.DB, dist batchDistiller, emb batchEmbedder) backfillDeps {
	t.Helper()
	return backfillDeps{
		cfg: config.VectorMemoryConfig{
			EmbedModel: "text-embedding-3-small",
			EmbedDim:   1536,
		},
		db:        database,
		distiller: dist,
		embedder:  emb,
		repo:      vectormemory.NewSQLiteRepository(database),
		chat:      chat.NewSQLiteRepository(database),
		logger:    log.New(io.Discard, "", 0),
		now:       time.Now,
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

func countUndistilled(t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT count(*) FROM chat_sessions WHERE memory_distilled_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count undistilled: %v", err)
	}
	return n
}

// TestBackfillEndToEnd seeds two sessions, runs the backfill with fake batch
// providers, and asserts memories were inserted and both sessions marked.
func TestBackfillEndToEnd(t *testing.T) {
	database := dbtest.New(t)
	seedSession(t, database, "s1", "u1")
	seedSession(t, database, "s2", "u2")

	// s1 yields two observations, s2 yields one. Sessions are ordered by
	// last_message_at ASC; both share roughly the same time, but the result
	// shape is index-aligned regardless.
	dist := &fakeDistiller{out: [][]string{
		{"Travels for work most weeks.", "Trains in hotel gyms."},
		{"Cutting for a meet."},
	}}
	emb := &fakeEmbedder{}

	if err := backfill(context.Background(), newDeps(t, database, dist, emb), false); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if dist.seen != 2 {
		t.Errorf("distiller saw %d conversations, want 2", dist.seen)
	}
	if emb.seen != 3 {
		t.Errorf("embedder saw %d observations, want 3", emb.seen)
	}
	if got := countMemories(t, database); got != 3 {
		t.Errorf("inserted %d memories, want 3", got)
	}
	if got := countUndistilled(t, database); got != 0 {
		t.Errorf("%d sessions still undistilled, want 0", got)
	}
}

// TestBackfillIdempotent verifies a second run is a no-op: the first run marks
// every session, so the second finds nothing and inserts nothing more.
func TestBackfillIdempotent(t *testing.T) {
	database := dbtest.New(t)
	seedSession(t, database, "s1", "u1")

	dist := &fakeDistiller{out: [][]string{{"Travels for work most weeks."}}}
	if err := backfill(context.Background(), newDeps(t, database, dist, &fakeEmbedder{}), false); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if got := countMemories(t, database); got != 1 {
		t.Fatalf("after first run: %d memories, want 1", got)
	}

	// Second run: nothing undistilled, so the distiller must not be called.
	dist2 := &fakeDistiller{out: nil}
	if err := backfill(context.Background(), newDeps(t, database, dist2, &fakeEmbedder{}), false); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if dist2.seen != 0 {
		t.Errorf("second run distilled %d conversations, want 0", dist2.seen)
	}
	if got := countMemories(t, database); got != 1 {
		t.Errorf("after second run: %d memories, want 1 (no new inserts)", got)
	}
}

// TestBackfillDryRun verifies dry-run writes nothing and leaves sessions
// undistilled, while still reporting (via the distiller/embedder being called).
func TestBackfillDryRun(t *testing.T) {
	database := dbtest.New(t)
	seedSession(t, database, "s1", "u1")

	dist := &fakeDistiller{out: [][]string{{"Travels for work most weeks."}}}
	emb := &fakeEmbedder{}
	if err := backfill(context.Background(), newDeps(t, database, dist, emb), true); err != nil {
		t.Fatalf("dry-run backfill: %v", err)
	}

	if emb.seen != 1 {
		t.Errorf("embedder saw %d, want 1 (dry-run still embeds)", emb.seen)
	}
	if got := countMemories(t, database); got != 0 {
		t.Errorf("dry-run inserted %d memories, want 0", got)
	}
	if got := countUndistilled(t, database); got != 1 {
		t.Errorf("dry-run marked sessions: %d undistilled, want 1", got)
	}
}
