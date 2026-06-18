package nutritionlookup

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// Counters live in the default registry and accumulate across tests,
// so every assertion here is a before/after delta.
func counterDelta(t *testing.T, read func() float64, action func()) float64 {
	t.Helper()
	before := read()
	action()
	return read() - before
}

func TestMetricsCacheHitPath(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	svc := NewService(repo, testLogger(), &fakeProvider{})
	ctx := context.Background()
	if err := repo.Put(ctx, CacheRow{
		QueryNormalized: "metrics big mac",
		CandidatesJSON:  `[{"name":"Big Mac","per_serving":{"calories":590,"protein_g":25,"fat_g":34,"carbs_g":46},"source":"fatsecret"}]`,
		FetchedAt:       svc.now().UTC(),
		LastUsedAt:      svc.now().UTC(),
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	hits := func() float64 {
		return testutil.ToFloat64(cacheEventsTotal.WithLabelValues("hit"))
	}
	outcomes := func() float64 {
		return testutil.ToFloat64(lookupRequestsTotal.WithLabelValues("cache_hit"))
	}
	lookup := func() {
		if _, err := svc.Lookup(ctx, "metrics  BIG mac", 1, 5); err != nil {
			t.Fatalf("lookup: %v", err)
		}
	}

	if d := counterDelta(t, hits, lookup); d != 1 {
		t.Errorf("cache hit events delta = %v, want 1", d)
	}
	if d := counterDelta(t, outcomes, lookup); d != 1 {
		t.Errorf("cache_hit outcome delta = %v, want 1", d)
	}
}

func TestMetricsUnavailableOutcome(t *testing.T) {
	svc := NewService(NewSQLiteRepository(dbtest.New(t)), testLogger(), &fakeProvider{})
	read := func() float64 {
		return testutil.ToFloat64(lookupRequestsTotal.WithLabelValues("unavailable"))
	}
	d := counterDelta(t, read, func() {
		_, _ = svc.Lookup(context.Background(), "metrics nothing configured", 1, 5)
	})
	if d != 1 {
		t.Errorf("unavailable outcome delta = %v, want 1", d)
	}
}

func TestMetricsProviderErrorAndServedPaths(t *testing.T) {
	broken := &fakeProvider{source: "fatsecret", configured: true, err: errAlwaysDown}
	working := &fakeProvider{
		source:     "usda",
		configured: true,
		hits: []Candidate{newCandidate(
			"Egg", "", "100 g",
			Macros{Calories: 212, ProteinG: 13.8, FatG: 16.2, CarbsG: 2.1},
			"usda", "9999",
		)},
	}
	svc := NewService(NewSQLiteRepository(dbtest.New(t)), testLogger(), broken, working)

	errCount := func() float64 {
		return testutil.ToFloat64(providerRequestsTotal.WithLabelValues("fatsecret", "error"))
	}
	okCount := func() float64 {
		return testutil.ToFloat64(providerRequestsTotal.WithLabelValues("usda", "ok"))
	}
	served := func() float64 {
		return testutil.ToFloat64(lookupRequestsTotal.WithLabelValues("served"))
	}
	writesOK := func() float64 {
		return testutil.ToFloat64(cacheWritesTotal.WithLabelValues("ok"))
	}

	before := []float64{errCount(), okCount(), served(), writesOK()}
	if _, err := svc.Lookup(context.Background(), "metrics scrambled eggs", 2, 5); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	after := []float64{errCount(), okCount(), served(), writesOK()}

	for i, name := range []string{"fatsecret error", "usda ok", "served outcome", "cache write ok"} {
		if d := after[i] - before[i]; d != 1 {
			t.Errorf("%s delta = %v, want 1", name, d)
		}
	}
}
