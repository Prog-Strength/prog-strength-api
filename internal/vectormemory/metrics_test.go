package vectormemory

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// Collectors live in the default registry and accumulate across tests, so
// every assertion here is a before/after delta on a specific label set.
func metricDelta(t *testing.T, c prometheus.Collector, action func()) float64 {
	t.Helper()
	before := testutil.ToFloat64(c)
	action()
	return testutil.ToFloat64(c) - before
}

// TestMetrics_CleanSweep covers the happy path: one selected session distilled
// into one inserted observation advances last_success, the success sweep
// counter, the session/observation counters, and the selected counter.
func TestMetrics_CleanSweep(t *testing.T) {
	ctx := context.Background()
	emb := &fakeEmbedder{vectors: map[string][]float32{"likes squats": oneHot(0)}}
	dis := &fakeDistiller{observations: []string{"likes squats"}}

	// cleanSweep runs one happy-path sweep against a fresh store so the
	// per-metric delta around it is exactly the work of a single tick.
	cleanSweep := func() {
		db := dbtest.New(t)
		repo := NewSQLiteRepository(db)
		seedSession(t, db, "s1", "userA")
		svc := NewService(repo, emb, dis, baseCfg(), testLogger())
		src := &fakeSessionSource{
			idle:          []IdleSession{{ID: "s1", UserID: "userA"}},
			conversations: map[string][]ConversationMessage{"s1": {{Role: "user", Content: "I love squats"}}},
		}
		if err := svc.distillOnce(ctx, src); err != nil {
			t.Fatalf("distillOnce: %v", err)
		}
	}

	for name, c := range map[string]prometheus.Collector{
		"sweeps_success":         sweepsTotal.WithLabelValues("success"),
		"sessions_selected":      sessionsSelectedTotal,
		"sessions_distilled":     sessionsDistilledTotal,
		"observations_distilled": observationsDistilledTotal,
		"observations_inserted":  observationsInsertedTotal,
	} {
		if d := metricDelta(t, c, cleanSweep); d != 1 {
			t.Errorf("%s delta = %v, want 1", name, d)
		}
	}

	// last_success advances on a clean sweep.
	beforeSuccessTS := testutil.ToFloat64(lastSuccessTimestamp)
	cleanSweep()
	if testutil.ToFloat64(lastSuccessTimestamp) < beforeSuccessTS {
		t.Errorf("last_success_timestamp did not advance on clean sweep")
	}
}

// TestMetrics_DistillerErrorClassifiesPartial proves a per-session distill
// failure increments stage_errors{stage="distill"} and classifies the sweep
// "partial" (the loop completed but a session broke).
func TestMetrics_DistillerErrorClassifiesPartial(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	dis := &fakeDistiller{errOn: true}
	svc := NewService(repo, &fakeEmbedder{}, dis, baseCfg(), testLogger())
	src := &fakeSessionSource{
		idle:          []IdleSession{{ID: "s1", UserID: "userA"}},
		conversations: map[string][]ConversationMessage{"s1": {{Role: "user", Content: "hi"}}},
	}

	partialBefore := testutil.ToFloat64(sweepsTotal.WithLabelValues("partial"))
	stageBefore := testutil.ToFloat64(stageErrorsTotal.WithLabelValues("distill"))

	if err := svc.distillOnce(ctx, src); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	if got := testutil.ToFloat64(sweepsTotal.WithLabelValues("partial")) - partialBefore; got != 1 {
		t.Errorf("partial sweep delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(stageErrorsTotal.WithLabelValues("distill")) - stageBefore; got != 1 {
		t.Errorf("stage_errors{distill} delta = %v, want 1", got)
	}
}

// TestMetrics_EmbedErrorRecordsStage proves an embed failure inside
// DistillSession increments stage_errors{stage="embed"}.
func TestMetrics_EmbedErrorRecordsStage(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	emb := &fakeEmbedder{errOn: true}
	dis := &fakeDistiller{observations: []string{"durable fact"}}
	svc := NewService(repo, emb, dis, baseCfg(), testLogger())

	d := metricDelta(t, stageErrorsTotal.WithLabelValues("embed"), func() {
		if _, err := svc.DistillSession(ctx, "userA", "s1", []ConversationMessage{{Role: "user", Content: "x"}}); err == nil {
			t.Fatal("expected embed error")
		}
	})
	if d != 1 {
		t.Errorf("stage_errors{embed} delta = %v, want 1", d)
	}
}

// TestMetrics_InsertErrorRecordsStageButSweepSucceeds proves a per-observation
// insert failure increments stage_errors{stage="insert"} yet leaves the sweep
// classified "success" — insert loss surfaces via the observation-counter gap,
// not the sweep result (DistillSession returns nil per its insert policy).
func TestMetrics_InsertErrorRecordsStageButSweepSucceeds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	emb := &fakeEmbedder{vectors: map[string][]float32{
		"first":  oneHot(0),
		"second": oneHot(1),
	}}
	dis := &fakeDistiller{observations: []string{"first", "second"}}
	frepo := &failInsertRepo{Repository: repo, failText: "second"}
	svc := NewService(frepo, emb, dis, baseCfg(), testLogger())
	src := &fakeSessionSource{
		idle:          []IdleSession{{ID: "s1", UserID: "userA"}},
		conversations: map[string][]ConversationMessage{"s1": {{Role: "user", Content: "x"}}},
	}

	insertErrDelta := testutil.ToFloat64(stageErrorsTotal.WithLabelValues("insert"))
	successDelta := testutil.ToFloat64(sweepsTotal.WithLabelValues("success"))
	insertedDelta := testutil.ToFloat64(observationsInsertedTotal)

	if err := svc.distillOnce(ctx, src); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}

	if got := testutil.ToFloat64(stageErrorsTotal.WithLabelValues("insert")) - insertErrDelta; got != 1 {
		t.Errorf("stage_errors{insert} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(sweepsTotal.WithLabelValues("success")) - successDelta; got != 1 {
		t.Errorf("success sweep delta = %v, want 1 (insert loss must not flip to partial)", got)
	}
	if got := testutil.ToFloat64(observationsInsertedTotal) - insertedDelta; got != 1 {
		t.Errorf("observations_inserted delta = %v, want 1 (the surviving observation)", got)
	}
}

// TestMetrics_SelectErrorClassifiesError proves a batch select failure
// increments stage_errors{stage="select"} and sweeps_total{result="error"} and
// leaves last_success unchanged.
func TestMetrics_SelectErrorClassifiesError(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewSQLiteRepository(dbtest.New(t)), &fakeEmbedder{}, &fakeDistiller{}, baseCfg(), testLogger())
	src := &fakeSessionSource{selectErr: errors.New("select boom")}

	beforeSuccessTS := testutil.ToFloat64(lastSuccessTimestamp)
	selectErrDelta := testutil.ToFloat64(stageErrorsTotal.WithLabelValues("select"))
	errSweepDelta := testutil.ToFloat64(sweepsTotal.WithLabelValues("error"))

	if err := svc.distillOnce(ctx, src); err == nil {
		t.Fatal("expected select error from distillOnce")
	}

	if got := testutil.ToFloat64(stageErrorsTotal.WithLabelValues("select")) - selectErrDelta; got != 1 {
		t.Errorf("stage_errors{select} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(sweepsTotal.WithLabelValues("error")) - errSweepDelta; got != 1 {
		t.Errorf("error sweep delta = %v, want 1", got)
	}
	if testutil.ToFloat64(lastSuccessTimestamp) != beforeSuccessTS {
		t.Errorf("last_success_timestamp must not advance on select error")
	}
}

// TestMetrics_IdleSessionsGauge proves the gauge reflects the count method's
// full backlog, not the (capped) selected batch size.
func TestMetrics_IdleSessionsGauge(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	backlog := 137
	svc := NewService(repo, &fakeEmbedder{}, &fakeDistiller{}, baseCfg(), testLogger())
	src := &fakeSessionSource{
		idle:          []IdleSession{{ID: "s1", UserID: "userA"}},
		conversations: map[string][]ConversationMessage{"s1": {{Role: "user", Content: "hi"}}},
		countOverride: &backlog,
	}

	if err := svc.distillOnce(ctx, src); err != nil {
		t.Fatalf("distillOnce: %v", err)
	}
	if got := testutil.ToFloat64(idleSessions); got != float64(backlog) {
		t.Errorf("idle_sessions gauge = %v, want %d", got, backlog)
	}
}

// TestMetrics_TokenCounters proves the distill/embed token counters parse the
// usage blocks the providers report.
func TestMetrics_TokenCounters(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	emb := &fakeEmbedder{
		vectors: map[string][]float32{"fact": oneHot(0)},
		usage:   EmbedUsage{TotalTokens: 64},
	}
	dis := &fakeDistiller{
		observations: []string{"fact"},
		usage:        DistillUsage{InputTokens: 400, OutputTokens: 25},
	}
	svc := NewService(repo, emb, dis, baseCfg(), testLogger())

	inDelta := testutil.ToFloat64(distillTokensTotal.WithLabelValues("input"))
	outDelta := testutil.ToFloat64(distillTokensTotal.WithLabelValues("output"))
	embDelta := testutil.ToFloat64(embedTokensTotal)

	if _, err := svc.DistillSession(ctx, "userA", "s1", []ConversationMessage{{Role: "user", Content: "x"}}); err != nil {
		t.Fatalf("DistillSession: %v", err)
	}

	if got := testutil.ToFloat64(distillTokensTotal.WithLabelValues("input")) - inDelta; got != 400 {
		t.Errorf("distill_tokens{input} delta = %v, want 400", got)
	}
	if got := testutil.ToFloat64(distillTokensTotal.WithLabelValues("output")) - outDelta; got != 25 {
		t.Errorf("distill_tokens{output} delta = %v, want 25", got)
	}
	if got := testutil.ToFloat64(embedTokensTotal) - embDelta; got != 64 {
		t.Errorf("embed_tokens delta = %v, want 64", got)
	}
}

// TestMetrics_DedupCounter proves a skipped near-duplicate increments
// observations_deduped without inserting.
func TestMetrics_DedupCounter(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	repo := NewSQLiteRepository(db)
	seedSession(t, db, "s1", "userA")

	if _, err := repo.Insert(ctx, newMem("userA", "s1", oneHot(0))); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	cfg := baseCfg()
	cfg.DedupThreshold = 0.5
	emb := &fakeEmbedder{vectors: map[string][]float32{"dup": oneHot(0)}}
	dis := &fakeDistiller{observations: []string{"dup"}}
	svc := NewService(repo, emb, dis, cfg, testLogger())

	d := metricDelta(t, observationsDedupedTotal, func() {
		if _, err := svc.DistillSession(ctx, "userA", "s1", []ConversationMessage{{Role: "user", Content: "x"}}); err != nil {
			t.Fatalf("DistillSession: %v", err)
		}
	})
	if d != 1 {
		t.Errorf("observations_deduped delta = %v, want 1", d)
	}
}
