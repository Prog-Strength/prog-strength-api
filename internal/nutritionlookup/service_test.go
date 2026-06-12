package nutritionlookup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// newMigratedDB opens a fresh migrated database in a temp dir with
// foreign keys on, mirroring internal/activity/sqlite_repository_test.go.
// Each test gets its own file so they run in parallel without sharing
// schema state.
func newMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn
}

// fakeProvider is a scriptable Provider with a call counter so tests
// can pin cache-hit behavior.
type fakeProvider struct {
	source     string
	configured bool
	hits       []Candidate
	err        error
	calls      int
}

func (p *fakeProvider) Source() string   { return p.source }
func (p *fakeProvider) Configured() bool { return p.configured }

func (p *fakeProvider) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if len(p.hits) > limit {
		return p.hits[:limit], nil
	}
	return p.hits, nil
}

func fsCandidate(name string, per Macros) Candidate {
	return newCandidate(name, "Chick-fil-A", "1 mini", per, "fatsecret", "12345")
}

func usdaCandidate(name string, per Macros) Candidate {
	return newCandidate(name, "", "100 g", per, "usda", "9999")
}

func TestServiceScalesQuantityInCode(t *testing.T) {
	fs := &fakeProvider{
		source:     "fatsecret",
		configured: true,
		hits:       []Candidate{fsCandidate("Chick-n-Mini", Macros{Calories: 90, ProteinG: 4.75, FatG: 3.25, CarbsG: 10.25})},
	}
	usda := &fakeProvider{source: "usda", configured: true}
	svc := NewService(NewMemoryRepository(), testLogger(), fs, usda)

	result, err := svc.Lookup(context.Background(), "chick fil a chicken mini", 10, 5)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1", len(result.Matches))
	}
	match := result.Matches[0]
	if match.PerServing.Calories != 90 {
		t.Errorf("PerServing.Calories = %v, want 90", match.PerServing.Calories)
	}
	want := Macros{Calories: 900, ProteinG: 47.5, FatG: 32.5, CarbsG: 102.5}
	if match.TotalForQuantity != want {
		t.Errorf("TotalForQuantity = %+v, want %+v", match.TotalForQuantity, want)
	}
	if result.Quantity != 10 {
		t.Errorf("Quantity = %v, want 10", result.Quantity)
	}
	// FatSecret returned fewer than max_results, so USDA is consulted too.
	if usda.calls != 1 {
		t.Errorf("usda.calls = %d, want 1 (appended while short of maxResults)", usda.calls)
	}
}

func TestServiceMergesFatSecretFirstThenUSDA(t *testing.T) {
	fs := &fakeProvider{
		source:     "fatsecret",
		configured: true,
		hits:       []Candidate{fsCandidate("Chick-n-Minis", Macros{Calories: 360, ProteinG: 19, FatG: 13, CarbsG: 41})},
	}
	usda := &fakeProvider{
		source:     "usda",
		configured: true,
		hits:       []Candidate{usdaCandidate("Egg, scrambled", Macros{Calories: 212, ProteinG: 13.8, FatG: 16.2, CarbsG: 2.1})},
	}
	svc := NewService(NewMemoryRepository(), testLogger(), fs, usda)

	result, err := svc.Lookup(context.Background(), "chicken minis", 1, 5)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("len(Matches) = %d, want 2", len(result.Matches))
	}
	if result.Matches[0].Source != "fatsecret" || result.Matches[1].Source != "usda" {
		t.Errorf("merge order = [%s, %s], want [fatsecret, usda]",
			result.Matches[0].Source, result.Matches[1].Source)
	}
}

func TestServiceFallsBackToUSDAWhenFatSecretEmpty(t *testing.T) {
	fs := &fakeProvider{source: "fatsecret", configured: true} // returns no hits
	usda := &fakeProvider{
		source:     "usda",
		configured: true,
		hits:       []Candidate{usdaCandidate("Egg, scrambled", Macros{Calories: 212, ProteinG: 13.8, FatG: 16.2, CarbsG: 2.1})},
	}
	svc := NewService(NewMemoryRepository(), testLogger(), fs, usda)

	result, err := svc.Lookup(context.Background(), "scrambled eggs", 2, 5)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Source != "usda" {
		t.Fatalf("Matches = %+v, want exactly one usda match", result.Matches)
	}
}

func TestServiceSurvivesOneProviderErroring(t *testing.T) {
	fs := &fakeProvider{source: "fatsecret", configured: true, err: errors.New("token endpoint 500")}
	usda := &fakeProvider{
		source:     "usda",
		configured: true,
		hits:       []Candidate{usdaCandidate("Egg, scrambled", Macros{Calories: 212, ProteinG: 13.8, FatG: 16.2, CarbsG: 2.1})},
	}
	svc := NewService(NewMemoryRepository(), testLogger(), fs, usda)

	result, err := svc.Lookup(context.Background(), "eggs", 1, 5)
	if err != nil {
		t.Fatalf("Lookup: %v (one healthy provider should be enough)", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Source != "usda" {
		t.Fatalf("Matches = %+v, want exactly one usda match", result.Matches)
	}
}

func TestServiceAllProvidersFailingReturnsErrFailed(t *testing.T) {
	fs := &fakeProvider{source: "fatsecret", configured: true, err: errors.New("token endpoint 500")}
	usda := &fakeProvider{source: "usda", configured: true, err: errors.New("search 500")}
	svc := NewService(NewMemoryRepository(), testLogger(), fs, usda)

	_, err := svc.Lookup(context.Background(), "eggs", 1, 5)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	if !strings.Contains(err.Error(), "fatsecret") || !strings.Contains(err.Error(), "usda") {
		t.Errorf("err = %q, want both provider names in the detail", err.Error())
	}
}

func TestServiceNoProvidersConfiguredReturnsErrUnavailable(t *testing.T) {
	fs := &fakeProvider{source: "fatsecret"}
	usda := &fakeProvider{source: "usda"}
	svc := NewService(NewMemoryRepository(), testLogger(), fs, usda)

	_, err := svc.Lookup(context.Background(), "eggs", 1, 5)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if fs.calls != 0 || usda.calls != 0 {
		t.Errorf("provider calls = %d/%d, want 0/0 (unconfigured providers must not be searched)", fs.calls, usda.calls)
	}
}

func TestServiceCacheHitSkipsProviders(t *testing.T) {
	fs := &fakeProvider{
		source:     "fatsecret",
		configured: true,
		hits:       []Candidate{newCandidate("Chick-n-Minis (4 Count)", "Chick-fil-A", "4 minis", Macros{Calories: 360, ProteinG: 19, FatG: 13, CarbsG: 41}, "fatsecret", "12345")},
	}
	svc := NewService(NewMemoryRepository(), testLogger(), fs)

	first, err := svc.Lookup(context.Background(), "Chicken Minis", 1, 5)
	if err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	// Different casing + whitespace normalizes to the same cache key;
	// different quantity scales after the cache.
	second, err := svc.Lookup(context.Background(), "chicken  MINIS", 2, 5)
	if err != nil {
		t.Fatalf("second Lookup: %v", err)
	}

	if fs.calls != 1 {
		t.Errorf("fs.calls = %d, want 1 (second lookup must hit cache)", fs.calls)
	}
	if got := first.Matches[0].TotalForQuantity.Calories; got != 360 {
		t.Errorf("first total calories = %v, want 360", got)
	}
	if got := second.Matches[0].TotalForQuantity.Calories; got != 720 {
		t.Errorf("second total calories = %v, want 720", got)
	}
	if second.Matches[0].Stale {
		t.Error("fresh cache hit must not be flagged stale")
	}
}

// seedCacheRow Puts a per-serving candidate row with the given
// fetched_at into repo under the normalized key.
func seedCacheRow(t *testing.T, repo Repository, key, name string, fetchedAt time.Time) {
	t.Helper()
	candidates := []Candidate{newCandidate(name, "", "1 serving", Macros{Calories: 100, ProteinG: 10, FatG: 2, CarbsG: 10}, "fatsecret", "1")}
	candidatesJSON, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}
	if err := repo.Put(context.Background(), CacheRow{
		QueryNormalized: key,
		CandidatesJSON:  string(candidatesJSON),
		FetchedAt:       fetchedAt,
		LastUsedAt:      fetchedAt,
	}); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}
}

func TestServiceStaleRowTriggersRePull(t *testing.T) {
	repo := NewMemoryRepository()
	// 8 days old — past the 7-day freshness TTL.
	seedCacheRow(t, repo, "chicken minis", "Old Cached Minis", time.Now().UTC().Add(-8*24*time.Hour))

	fs := &fakeProvider{
		source:     "fatsecret",
		configured: true,
		hits:       []Candidate{fsCandidate("Fresh Minis", Macros{Calories: 360, ProteinG: 19, FatG: 13, CarbsG: 41})},
	}
	svc := NewService(repo, testLogger(), fs)

	result, err := svc.Lookup(context.Background(), "chicken minis", 1, 5)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if fs.calls != 1 {
		t.Errorf("fs.calls = %d, want 1 (stale row must trigger a re-pull)", fs.calls)
	}
	if len(result.Matches) != 1 || result.Matches[0].Name != "Fresh Minis" {
		t.Fatalf("Matches = %+v, want the re-pulled candidate", result.Matches)
	}
	if result.Matches[0].Stale {
		t.Error("successfully re-pulled candidates must not be flagged stale")
	}

	// The successful pull overwrote the row: a follow-up lookup serves
	// the fresh data from cache.
	if _, err := svc.Lookup(context.Background(), "chicken minis", 1, 5); err != nil {
		t.Fatalf("follow-up Lookup: %v", err)
	}
	if fs.calls != 1 {
		t.Errorf("fs.calls = %d after follow-up, want 1 (refreshed row should serve from cache)", fs.calls)
	}
}

func TestServiceProviderFailurePlusStaleRowServesStale(t *testing.T) {
	repo := NewMemoryRepository()
	seedCacheRow(t, repo, "chicken minis", "Old Cached Minis", time.Now().UTC().Add(-8*24*time.Hour))

	fs := &fakeProvider{source: "fatsecret", configured: true, err: errors.New("token endpoint 500")}
	svc := NewService(repo, testLogger(), fs)

	result, err := svc.Lookup(context.Background(), "chicken minis", 3, 5)
	if err != nil {
		t.Fatalf("Lookup: %v (stale fallback should not error)", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("len(Matches) = %d, want 1", len(result.Matches))
	}
	match := result.Matches[0]
	if match.Name != "Old Cached Minis" {
		t.Errorf("Name = %q, want the stale cached candidate", match.Name)
	}
	if !match.Stale {
		t.Error("Stale = false, want true on candidates served past the TTL")
	}
	if match.TotalForQuantity.Calories != 300 {
		t.Errorf("TotalForQuantity.Calories = %v, want 300 (stale rows still scale)", match.TotalForQuantity.Calories)
	}
}

// --- SQLite repository ----------------------------------------------

func TestSQLiteRepositoryGetMissAndHitBump(t *testing.T) {
	conn := newMigratedDB(t)
	repo := NewSQLiteRepository(conn)
	ctx := context.Background()

	row, err := repo.Get(ctx, "never stored")
	if err != nil {
		t.Fatalf("Get miss: %v", err)
	}
	if row != nil {
		t.Fatalf("Get miss = %+v, want nil", row)
	}

	fetched := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	if err = repo.Put(ctx, CacheRow{
		QueryNormalized: "chicken minis",
		CandidatesJSON:  "[]",
		FetchedAt:       fetched,
		LastUsedAt:      fetched,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := repo.Get(ctx, "chicken minis")
	if err != nil {
		t.Fatalf("Get hit: %v", err)
	}
	if got == nil {
		t.Fatal("Get hit = nil, want row")
	}
	if !got.FetchedAt.Equal(fetched) {
		t.Errorf("FetchedAt = %v, want %v (Get must not touch fetched_at)", got.FetchedAt, fetched)
	}
	if !got.LastUsedAt.Equal(now) {
		t.Errorf("LastUsedAt = %v, want %v (Get must bump last_used_at)", got.LastUsedAt, now)
	}

	// The bump persisted, not just decorated the return value.
	var persisted time.Time
	if err := conn.QueryRowContext(ctx,
		`SELECT last_used_at FROM nutrition_lookup_cache WHERE query_normalized = ?`,
		"chicken minis").Scan(&persisted); err != nil {
		t.Fatalf("read back last_used_at: %v", err)
	}
	if !persisted.Equal(now) {
		t.Errorf("persisted last_used_at = %v, want %v", persisted, now)
	}
}

func TestSQLiteRepositoryPutEvictsLongUnusedRows(t *testing.T) {
	conn := newMigratedDB(t)
	repo := NewSQLiteRepository(conn)
	ctx := context.Background()

	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-91 * 24 * time.Hour)

	// Seed the old row with the clock wound back so the seeding Put's
	// own sweep doesn't remove it.
	repo.now = func() time.Time { return old }
	if err := repo.Put(ctx, CacheRow{
		QueryNormalized: "one-off food",
		CandidatesJSON:  "[]",
		FetchedAt:       old,
		LastUsedAt:      old,
	}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	// A write 91 days later piggybacks the eviction sweep.
	repo.now = func() time.Time { return now }
	if err := repo.Put(ctx, CacheRow{
		QueryNormalized: "daily food",
		CandidatesJSON:  "[]",
		FetchedAt:       now,
		LastUsedAt:      now,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	evicted, err := repo.Get(ctx, "one-off food")
	if err != nil {
		t.Fatalf("Get evicted: %v", err)
	}
	if evicted != nil {
		t.Errorf("row with last_used_at 91 days old survived the sweep: %+v", evicted)
	}
	kept, err := repo.Get(ctx, "daily food")
	if err != nil {
		t.Fatalf("Get kept: %v", err)
	}
	if kept == nil {
		t.Error("fresh row was evicted, want kept")
	}
}

// --- Plausibility math ----------------------------------------------

func TestPlausibilityWarning(t *testing.T) {
	tests := []struct {
		name string
		per  Macros
		warn bool
	}{
		// 360 kcal vs 4*19 + 4*41 + 9*13 = 357 — well within 25%.
		{"consistent macros", Macros{Calories: 360, ProteinG: 19, FatG: 13, CarbsG: 41}, false},
		// 900 stated vs 4*10 + 4*20 + 9*5 = 165 derived.
		{"divergent macros", Macros{Calories: 900, ProteinG: 10, FatG: 5, CarbsG: 20}, true},
		// Diet drinks etc. — ratio math is meaningless under the floor.
		{"tiny calorie items skipped", Macros{Calories: 5, ProteinG: 0, FatG: 0, CarbsG: 2}, false},
		{"floor boundary", Macros{Calories: 20, ProteinG: 0, FatG: 0, CarbsG: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plausibilityWarning(tt.per)
			if tt.warn && got == "" {
				t.Fatal("plausibilityWarning = \"\", want a warning")
			}
			if !tt.warn && got != "" {
				t.Fatalf("plausibilityWarning = %q, want none", got)
			}
			if tt.warn && !strings.Contains(got, "diverge") {
				t.Errorf("warning %q does not contain %q", got, "diverge")
			}
		})
	}
}
