package vectormemory

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

const (
	embedDim    = 1536
	activeModel = "text-embedding-3-small"
)

// oneHot builds a 1536-dim vector with 1.0 at index i and 0.0 elsewhere.
// Two distinct one-hot vectors are orthogonal, so their cosine distance is
// 1.0; a vector's distance to itself is 0.0.
func oneHot(i int) []float32 {
	v := make([]float32, embedDim)
	v[i] = 1
	return v
}

// twoHot builds a 1536-dim vector with 1.0 at indices i and j. Against a
// one-hot at i its cosine similarity is 1/sqrt(2), so cosine distance is
// 1 - 1/sqrt(2) ~= 0.293 — an intermediate distance between 0 and 1 useful
// for exercising the threshold cap.
func twoHot(i, j int) []float32 {
	v := make([]float32, embedDim)
	v[i] = 1
	v[j] = 1
	return v
}

// seedSession inserts the chat_sessions row Insert's FK requires.
func seedSession(t *testing.T, db *sql.DB, id, userID string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := db.Exec(`
		INSERT INTO chat_sessions (id, user_id, title, created_at, updated_at, last_message_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, userID, "", now, now, now); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// seedMessage inserts a chat_messages row in the given session and returns
// its id, so a non-nil source_message_id satisfies its FK.
func seedMessage(t *testing.T, db *sql.DB, sessionID string) *int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO chat_messages (session_id, position, role, content, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, sessionID, 0, "user", "hi", time.Now().UTC())
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed message id: %v", err)
	}
	return &id
}

func newMem(userID, sessionID string, vec []float32) NewMemory {
	return NewMemory{
		UserID:          userID,
		DistilledText:   "memory for " + sessionID,
		SourceSessionID: sessionID,
		EmbeddingModel:  activeModel,
		EmbeddingDim:    embedDim,
		Embedding:       vec,
		CreatedAt:       time.Now().UTC(),
	}
}

func TestInsertThenDump(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")
	msgID := seedMessage(t, db, "s1")

	in := NewMemory{
		UserID:          "userA",
		DistilledText:   "prefers 5x5 squats",
		SourceSessionID: "s1",
		SourceMessageID: msgID,
		EmbeddingModel:  activeModel,
		EmbeddingDim:    embedDim,
		Embedding:       oneHot(0),
		CreatedAt:       time.Now().UTC(),
	}
	id, err := repo.Insert(ctx, in)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	got, err := repo.Dump(ctx, "userA", 10, 0)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	m := got[0]
	if m.ID != id || m.UserID != "userA" || m.DistilledText != "prefers 5x5 squats" ||
		m.SourceSessionID != "s1" || m.EmbeddingModel != activeModel || m.EmbeddingDim != embedDim {
		t.Fatalf("dumped row mismatch: %+v", m)
	}
	if m.SourceMessageID == nil || *m.SourceMessageID != *msgID {
		t.Fatalf("expected source_message_id %v, got %v", msgID, m.SourceMessageID)
	}
	if m.SupersededAt != nil {
		t.Fatalf("expected superseded_at nil, got %v", m.SupersededAt)
	}

	var vecCount int
	if err := db.QueryRow(`SELECT count(*) FROM vec_agent_memories WHERE memory_id = ?`, id).Scan(&vecCount); err != nil {
		t.Fatalf("count vec rows: %v", err)
	}
	if vecCount != 1 {
		t.Fatalf("expected 1 vec row, got %d", vecCount)
	}
}

func TestSearchPerUserScoping(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "sA", "userA")
	seedSession(t, db, "sB", "userB")

	// userA: three distinct one-hot vectors. The query oneHot(0) is identical
	// to the first (distance 0) and orthogonal to the other two (distance 1).
	for _, idx := range []int{0, 1, 2} {
		if _, err := repo.Insert(ctx, newMem("userA", "sA", oneHot(idx))); err != nil {
			t.Fatalf("insert userA %d: %v", idx, err)
		}
	}
	if _, err := repo.Insert(ctx, newMem("userB", "sB", oneHot(0))); err != nil {
		t.Fatalf("insert userB: %v", err)
	}

	matches, err := repo.Search(ctx, "userA", activeModel, oneHot(0), 5, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("expected 3 matches for userA, got %d", len(matches))
	}
	for i, m := range matches {
		if m.SourceSessionID != "sA" {
			t.Fatalf("match %d not scoped to userA: session %s", i, m.SourceSessionID)
		}
		if i > 0 && matches[i-1].Distance > m.Distance {
			t.Fatalf("matches not ascending by distance: %v", matches)
		}
	}
	if matches[0].Distance != 0 {
		t.Fatalf("expected nearest distance 0, got %v", matches[0].Distance)
	}
}

func TestSearchThresholdCap(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")

	// Nearest is the exact match (distance 0); the rest are orthogonal
	// (distance 1). A cap of 0.5 sits between them, so only the nearest
	// should be returned.
	for _, idx := range []int{0, 1, 2} {
		if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(idx))); err != nil {
			t.Fatalf("insert %d: %v", idx, err)
		}
	}

	matches, err := repo.Search(ctx, "userA", activeModel, oneHot(0), 10, 0.5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match under threshold 0.5, got %d", len(matches))
	}
	if matches[0].Distance != 0 {
		t.Fatalf("expected nearest distance 0, got %v", matches[0].Distance)
	}
}

func TestSearchKCap(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")

	// Four vectors all within an intermediate distance of the query: the
	// query is twoHot(0,1); each stored vector shares one of those hot
	// indices, so all four are under a generous threshold.
	vecs := [][]float32{twoHot(0, 1), twoHot(0, 2), twoHot(1, 3), twoHot(0, 4)}
	for _, v := range vecs {
		if _, err := repo.Insert(ctx, newMem("userA", "s", v)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	matches, err := repo.Search(ctx, "userA", activeModel, twoHot(0, 1), 2, 0.9)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected exactly 2 matches with k=2, got %d", len(matches))
	}
}

func TestSearchExcludesSuperseded(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")

	nearestID, insErr := repo.Insert(ctx, newMem("userA", "s", oneHot(0)))
	if insErr != nil {
		t.Fatalf("insert nearest: %v", insErr)
	}
	if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(1))); err != nil {
		t.Fatalf("insert other: %v", err)
	}

	if _, err := db.Exec(`UPDATE agent_memories SET superseded_at = ? WHERE id = ?`, time.Now().UTC(), nearestID); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	matches, err := repo.Search(ctx, "userA", activeModel, oneHot(0), 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match after superseding nearest, got %d", len(matches))
	}
	if matches[0].Distance == 0 {
		t.Fatalf("superseded exact-match row was returned (distance 0)")
	}
}

func TestSearchModelFilter(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")

	other := newMem("userA", "s", oneHot(0))
	other.EmbeddingModel = "other-model"
	if _, err := repo.Insert(ctx, other); err != nil {
		t.Fatalf("insert other-model: %v", err)
	}
	if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(1))); err != nil {
		t.Fatalf("insert active-model: %v", err)
	}

	matches, err := repo.Search(ctx, "userA", activeModel, oneHot(0), 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match (other-model excluded), got %d", len(matches))
	}
	if matches[0].Distance == 0 {
		t.Fatalf("other-model exact match leaked into results (distance 0)")
	}
}

func TestNearestDistance(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")

	if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(0))); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(1))); err != nil {
		t.Fatalf("insert: %v", err)
	}

	dist, ok, err := repo.NearestDistance(ctx, "userA", activeModel, oneHot(0))
	if err != nil {
		t.Fatalf("nearest distance: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for user with memories")
	}
	if dist != 0 {
		t.Fatalf("expected nearest distance 0, got %v", dist)
	}

	_, ok, err = repo.NearestDistance(ctx, "userNone", activeModel, oneHot(0))
	if err != nil {
		t.Fatalf("nearest distance (empty): %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for user with no memories")
	}
}
