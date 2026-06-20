package vectormemory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// fakeEmbedder maps known strings to fixed 1536-dim vectors via a deterministic
// table, returning one vector per input in order. With errOn set it fails the
// next Embed call, simulating a provider outage.
type fakeEmbedder struct {
	vectors map[string][]float32
	errOn   bool
}

func (f *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if f.errOn {
		return nil, errors.New("embed boom")
	}
	out := make([][]float32, 0, len(inputs))
	for _, in := range inputs {
		v, ok := f.vectors[in]
		if !ok {
			// Default to a vector orthogonal to everything keyed so far so an
			// unmapped string never accidentally collides with a known one.
			v = oneHot(embedDim - 1)
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeEmbedder) Configured() bool { return true }

// fakeDistiller returns a preset observation list (possibly empty) and can be
// switched to fail, simulating the LLM call erroring out.
type fakeDistiller struct {
	observations []string
	errOn        bool
}

func (f *fakeDistiller) Distill(_ context.Context, _ string) ([]string, error) {
	if f.errOn {
		return nil, errors.New("distill boom")
	}
	return f.observations, nil
}

func (f *fakeDistiller) Configured() bool { return true }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseCfg() config.VectorMemoryConfig {
	return config.VectorMemoryConfig{
		Enabled:    true,
		EmbedModel: activeModel,
		EmbedDim:   embedDim,
		TopK:       5,
	}
}

func TestServiceRetrieveOrderingAndThresholdSentinel(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s", "userA")

	// Three memories: an exact match (distance 0 to the query) and two
	// orthogonal ones (distance 1).
	for _, idx := range []int{0, 1, 2} {
		if _, err := repo.Insert(ctx, newMem("userA", "s", oneHot(idx))); err != nil {
			t.Fatalf("insert %d: %v", idx, err)
		}
	}

	// Query "q0" embeds to oneHot(0): identical to the first memory.
	emb := &fakeEmbedder{vectors: map[string][]float32{"q0": oneHot(0)}}
	dis := &fakeDistiller{}

	t.Run("config default threshold and TopK", func(t *testing.T) {
		// DistanceThreshold 0.5 sits between the exact match (0) and the
		// orthogonal ones (1); threshold<0 defers to it, so only the exact
		// match clears the cap. k<=0 falls back to cfg.TopK.
		cfg := baseCfg()
		cfg.DistanceThreshold = 0.5
		svc := NewService(repo, emb, dis, cfg, testLogger())

		got, err := svc.Retrieve(ctx, "userA", "q0", 0, -1)
		if err != nil {
			t.Fatalf("retrieve: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 match under config default cap, got %d", len(got))
		}
		if got[0].Distance != 0 {
			t.Fatalf("expected nearest distance 0, got %v", got[0].Distance)
		}
	})

	t.Run("zero threshold = full sweep", func(t *testing.T) {
		cfg := baseCfg()
		cfg.DistanceThreshold = 0.5 // must be ignored when threshold == 0
		svc := NewService(repo, emb, dis, cfg, testLogger())

		got, err := svc.Retrieve(ctx, "userA", "q0", 5, 0)
		if err != nil {
			t.Fatalf("retrieve: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("expected full neighborhood of 3, got %d", len(got))
		}
		// Ordered ascending by distance.
		for i := 1; i < len(got); i++ {
			if got[i-1].Distance > got[i].Distance {
				t.Fatalf("matches not ascending by distance: %+v", got)
			}
		}
		if got[0].Distance != 0 {
			t.Fatalf("expected nearest distance 0, got %v", got[0].Distance)
		}
	})

	t.Run("positive threshold caps", func(t *testing.T) {
		cfg := baseCfg()
		svc := NewService(repo, emb, dis, cfg, testLogger())

		got, err := svc.Retrieve(ctx, "userA", "q0", 5, 0.5)
		if err != nil {
			t.Fatalf("retrieve: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 match under explicit cap 0.5, got %d", len(got))
		}
	})

	t.Run("nothing clears the cap", func(t *testing.T) {
		cfg := baseCfg()
		// Query embeds to a vector orthogonal to every stored memory, so even
		// the nearest is at distance 1 > cap 0.5.
		orth := &fakeEmbedder{vectors: map[string][]float32{"far": oneHot(500)}}
		svc := NewService(repo, orth, dis, cfg, testLogger())

		got, err := svc.Retrieve(ctx, "userA", "far", 5, 0.5)
		if err != nil {
			t.Fatalf("retrieve: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected 0 matches when nothing clears cap, got %d", len(got))
		}
	})
}

func TestServiceRetrieveEmptyQuery(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)

	// errOn would make any embed call fail; an empty query must short-circuit
	// before embedding, so no error should surface.
	emb := &fakeEmbedder{errOn: true}
	svc := NewService(repo, emb, &fakeDistiller{}, baseCfg(), testLogger())

	got, err := svc.Retrieve(ctx, "userA", "", 5, -1)
	if err != nil {
		t.Fatalf("expected nil error for empty query, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil matches for empty query, got %v", got)
	}
}

func TestServiceRetrieveEmbedderError(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)

	emb := &fakeEmbedder{errOn: true}
	svc := NewService(repo, emb, &fakeDistiller{}, baseCfg(), testLogger())

	_, err := svc.Retrieve(ctx, "userA", "anything", 5, -1)
	if err == nil {
		t.Fatal("expected error from embedder failure")
	}
}

func TestServiceDistillSessionInserts(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	cfg := baseCfg() // DedupThreshold 0 ⇒ dedup off, insert everything

	t.Run("two observations insert two memories", func(t *testing.T) {
		emb := &fakeEmbedder{vectors: map[string][]float32{
			"likes squats":  oneHot(0),
			"trains monday": oneHot(1),
		}}
		dis := &fakeDistiller{observations: []string{"likes squats", "trains monday"}}
		svc := NewService(repo, emb, dis, cfg, testLogger())

		n, err := svc.DistillSession(ctx, "userA", "s1", []ConversationMessage{
			{Role: "user", Content: "I love squats"},
			{Role: "assistant", Content: "Noted."},
		})
		if err != nil {
			t.Fatalf("distill: %v", err)
		}
		if n != 2 {
			t.Fatalf("expected 2 inserts, got %d", n)
		}
		dumped, err := svc.Dump(ctx, "userA", 10, 0)
		if err != nil {
			t.Fatalf("dump: %v", err)
		}
		if len(dumped) != 2 {
			t.Fatalf("expected 2 memories in store, got %d", len(dumped))
		}
	})

	t.Run("empty observations insert nothing", func(t *testing.T) {
		db := dbtest.New(t)
		repo := NewSQLiteRepository(db)
		seedSession(t, db, "s2", "userB")

		emb := &fakeEmbedder{}
		dis := &fakeDistiller{observations: nil}
		svc := NewService(repo, emb, dis, cfg, testLogger())

		n, err := svc.DistillSession(ctx, "userB", "s2", []ConversationMessage{
			{Role: "user", Content: "hi"},
		})
		if err != nil {
			t.Fatalf("distill: %v", err)
		}
		if n != 0 {
			t.Fatalf("expected 0 inserts for empty observations, got %d", n)
		}
		dumped, err := svc.Dump(ctx, "userB", 10, 0)
		if err != nil {
			t.Fatalf("dump: %v", err)
		}
		if len(dumped) != 0 {
			t.Fatalf("expected empty store, got %d", len(dumped))
		}
	})
}

func TestServiceDistillSessionDedup(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	// Pre-existing memory at oneHot(0).
	if _, err := repo.Insert(ctx, newMem("userA", "s1", oneHot(0))); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	cfg := baseCfg()
	cfg.DedupThreshold = 0.5 // dedup on

	// Two observations: "dup" embeds to oneHot(0) (distance 0 to the existing
	// memory, within the 0.5 dedup cap ⇒ skipped); "fresh" embeds to a vector
	// orthogonal to every existing memory (distance 1 > cap ⇒ inserted).
	emb := &fakeEmbedder{vectors: map[string][]float32{
		"dup":   oneHot(0),
		"fresh": oneHot(100),
	}}
	dis := &fakeDistiller{observations: []string{"dup", "fresh"}}
	svc := NewService(repo, emb, dis, cfg, testLogger())

	n, err := svc.DistillSession(ctx, "userA", "s1", []ConversationMessage{
		{Role: "user", Content: "stuff"},
	})
	if err != nil {
		t.Fatalf("distill: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 insert (duplicate skipped), got %d", n)
	}

	dumped, err := svc.Dump(ctx, "userA", 10, 0)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	// Seed + the one fresh observation.
	if len(dumped) != 2 {
		t.Fatalf("expected 2 memories total, got %d", len(dumped))
	}
}

func TestServiceDistillSessionDistillerError(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	dis := &fakeDistiller{errOn: true}
	svc := NewService(repo, &fakeEmbedder{}, dis, baseCfg(), testLogger())

	n, err := svc.DistillSession(ctx, "userA", "s1", []ConversationMessage{
		{Role: "user", Content: "hi"},
	})
	if err == nil {
		t.Fatal("expected error from distiller failure")
	}
	if n != 0 {
		t.Fatalf("expected 0 inserts on distiller error, got %d", n)
	}
}
