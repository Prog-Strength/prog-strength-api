package weather

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// mustInsertUser inserts a minimal live user row so saved locations can
// satisfy the user_id foreign key (same helper shape as dashboard's tests).
func mustInsertUser(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO users (id, email, display_name, weight_unit, created_at, updated_at)
		VALUES (?, ?, ?, 'lb', '2026-01-01', '2026-01-01')
	`, userID, userID+"@example.com", userID)
	if err != nil {
		t.Fatalf("insert user %q: %v", userID, err)
	}
}

func newTestCacheRepo(t *testing.T, at time.Time) (*SQLiteCacheRepository, *time.Time) {
	t.Helper()
	repo := NewSQLiteCacheRepository(dbtest.New(t))
	current := at
	repo.now = func() time.Time { return current }
	return repo, &current
}

func TestCacheGetMissReturnsNilNil(t *testing.T) {
	repo, _ := newTestCacheRepo(t, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	row, err := repo.Get(context.Background(), "39.74:-104.98:current")
	if err != nil {
		t.Fatalf("Get on empty cache: %v", err)
	}
	if row != nil {
		t.Fatalf("Get on empty cache = %+v, want nil (miss is nil, nil)", row)
	}
}

func TestCachePutGetRoundTripBumpsLastUsed(t *testing.T) {
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repo, clock := newTestCacheRepo(t, t0)
	ctx := context.Background()

	put := CacheRow{
		CacheKey:    "39.74:-104.98:current",
		PayloadJSON: `{"temp_c":21.4}`,
		FetchedAt:   t0,
		LastUsedAt:  t0,
	}
	if err := repo.Put(ctx, put); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read an hour later: the payload round-trips and the read itself is
	// the eviction signal, so last_used_at moves to read time.
	*clock = t0.Add(time.Hour)
	got, err := repo.Get(ctx, put.CacheKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get after Put = nil, want a hit")
	}
	if got.PayloadJSON != put.PayloadJSON {
		t.Errorf("PayloadJSON = %q, want %q", got.PayloadJSON, put.PayloadJSON)
	}
	if !got.FetchedAt.Equal(t0) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, t0)
	}
	if !got.LastUsedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("LastUsedAt = %v, want bumped to %v", got.LastUsedAt, t0.Add(time.Hour))
	}

	// The bump must be durable, not just reflected in the returned struct.
	got2, err := repo.Get(ctx, put.CacheKey)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !got2.LastUsedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("stored LastUsedAt = %v, want %v", got2.LastUsedAt, t0.Add(time.Hour))
	}
}

func TestCachePutSweepsRowsUnusedFor90Days(t *testing.T) {
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repo, clock := newTestCacheRepo(t, t0)
	ctx := context.Background()

	stale := CacheRow{CacheKey: "stale", PayloadJSON: `{}`, FetchedAt: t0, LastUsedAt: t0}
	if err := repo.Put(ctx, stale); err != nil {
		t.Fatalf("Put stale: %v", err)
	}

	// 91 days later a new write sweeps anything unused past the 90-day age.
	*clock = t0.Add(91 * 24 * time.Hour)
	fresh := CacheRow{CacheKey: "fresh", PayloadJSON: `{}`, FetchedAt: *clock, LastUsedAt: *clock}
	if err := repo.Put(ctx, fresh); err != nil {
		t.Fatalf("Put fresh: %v", err)
	}

	got, err := repo.Get(ctx, "stale")
	if err != nil {
		t.Fatalf("Get stale: %v", err)
	}
	if got != nil {
		t.Errorf("stale row survived the sweep: %+v", got)
	}
	if got, err := repo.Get(ctx, "fresh"); err != nil || got == nil {
		t.Errorf("fresh row must survive the sweep (row=%v err=%v)", got, err)
	}
}

func TestReadingKeyRoundsToTwoDecimals(t *testing.T) {
	// ~1.1 km rounding: nearby coordinates share a cache row instead of
	// fragmenting the cache and burning budget.
	a := ReadingKey(39.741, -104.98, EndpointCurrent)
	b := ReadingKey(39.744, -104.98, EndpointCurrent)
	if a != b {
		t.Errorf("keys differ for near-identical coordinates: %q vs %q", a, b)
	}
	if want := "39.74:-104.98:current"; a != want {
		t.Errorf("ReadingKey = %q, want %q", a, want)
	}
}

func TestGeocodeKeysNormalize(t *testing.T) {
	if got, want := GeocodeDirectKey("Denver  CO"), GeocodeDirectKey("denver co"); got != want {
		t.Errorf("GeocodeDirectKey normalization: %q vs %q", got, want)
	}
	if got, want := GeocodeDirectKey("Denver  CO"), "geocode_direct:denver co"; got != want {
		t.Errorf("GeocodeDirectKey = %q, want %q", got, want)
	}
	if got, want := GeocodeReverseKey(39.741, -104.984), "geocode_reverse:39.74:-104.98"; got != want {
		t.Errorf("GeocodeReverseKey = %q, want %q", got, want)
	}
}

func TestCacheLastSuccess(t *testing.T) {
	t0 := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	repo, _ := newTestCacheRepo(t, t0)
	ctx := context.Background()

	got, err := repo.LastSuccess(ctx)
	if err != nil {
		t.Fatalf("LastSuccess on empty cache: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("LastSuccess on empty cache = %v, want zero time", got)
	}

	older := CacheRow{CacheKey: "a", PayloadJSON: `{}`, FetchedAt: t0.Add(-2 * time.Hour), LastUsedAt: t0}
	newer := CacheRow{CacheKey: "b", PayloadJSON: `{}`, FetchedAt: t0, LastUsedAt: t0}
	if err := repo.Put(ctx, older); err != nil {
		t.Fatalf("Put older: %v", err)
	}
	if err := repo.Put(ctx, newer); err != nil {
		t.Fatalf("Put newer: %v", err)
	}

	got, err = repo.LastSuccess(ctx)
	if err != nil {
		t.Fatalf("LastSuccess: %v", err)
	}
	if !got.Equal(t0) {
		t.Errorf("LastSuccess = %v, want newest fetched_at %v", got, t0)
	}
}

func strPtr(s string) *string { return &s }

func denverBoulder() []Location {
	return []Location{
		{Label: "Denver", Country: "US", State: strPtr("Colorado"), Lat: 39.7392, Lon: -104.9847},
		{Label: "Boulder", Country: "US", State: strPtr("Colorado"), Lat: 40.015, Lon: -105.2705},
	}
}

func TestLocationsReplaceAllAndListOrdered(t *testing.T) {
	database := dbtest.New(t)
	mustInsertUser(t, database, "user-a")
	repo := NewSQLiteLocationsRepository(database)
	ctx := context.Background()

	if err := repo.ReplaceAll(ctx, "user-a", denverBoulder()); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}

	got, err := repo.List(ctx, "user-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(got))
	}
	for i, loc := range got {
		if loc.Position != i {
			t.Errorf("locations[%d].Position = %d, want %d", i, loc.Position, i)
		}
		if loc.ID == "" {
			t.Errorf("locations[%d].ID is empty, want a generated id", i)
		}
		if loc.UserID != "user-a" {
			t.Errorf("locations[%d].UserID = %q, want user-a", i, loc.UserID)
		}
	}
	if got[0].Label != "Denver" || got[1].Label != "Boulder" {
		t.Errorf("labels = %q, %q, want Denver, Boulder (input order)", got[0].Label, got[1].Label)
	}
	if got[0].Lat != 39.7392 || got[0].Lon != -104.9847 {
		t.Errorf("Denver coords = %v,%v, want 39.7392,-104.9847", got[0].Lat, got[0].Lon)
	}
	if got[0].State == nil || *got[0].State != "Colorado" {
		t.Errorf("Denver State = %v, want Colorado", got[0].State)
	}
}

func TestLocationsReplaceAllReorderPreservesIDs(t *testing.T) {
	database := dbtest.New(t)
	mustInsertUser(t, database, "user-a")
	repo := NewSQLiteLocationsRepository(database)
	ctx := context.Background()

	if err := repo.ReplaceAll(ctx, "user-a", denverBoulder()); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}
	before, err := repo.List(ctx, "user-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Reorder: Boulder first. IDs and coordinates must come through intact,
	// only positions change.
	reordered := []Location{before[1], before[0]}
	if err := repo.ReplaceAll(ctx, "user-a", reordered); err != nil {
		t.Fatalf("ReplaceAll reordered: %v", err)
	}
	after, err := repo.List(ctx, "user-a")
	if err != nil {
		t.Fatalf("List after reorder: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(after))
	}
	if after[0].ID != before[1].ID || after[1].ID != before[0].ID {
		t.Errorf("ids not preserved across reorder: before %q,%q after %q,%q",
			before[0].ID, before[1].ID, after[0].ID, after[1].ID)
	}
	if after[0].Label != "Boulder" || after[1].Label != "Denver" {
		t.Errorf("labels after reorder = %q, %q, want Boulder, Denver", after[0].Label, after[1].Label)
	}
	if after[0].Position != 0 || after[1].Position != 1 {
		t.Errorf("positions after reorder = %d, %d, want 0, 1", after[0].Position, after[1].Position)
	}
	if after[0].Lat != 40.015 || after[0].Lon != -105.2705 {
		t.Errorf("Boulder coords = %v,%v, want 40.015,-105.2705", after[0].Lat, after[0].Lon)
	}
}

func TestLocationsReplaceAllEmptyClears(t *testing.T) {
	database := dbtest.New(t)
	mustInsertUser(t, database, "user-a")
	repo := NewSQLiteLocationsRepository(database)
	ctx := context.Background()

	if err := repo.ReplaceAll(ctx, "user-a", denverBoulder()); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}
	if err := repo.ReplaceAll(ctx, "user-a", nil); err != nil {
		t.Fatalf("ReplaceAll(empty): %v", err)
	}
	got, err := repo.List(ctx, "user-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(List) after clearing = %d, want 0", len(got))
	}
}

func TestLocationsCountSpansUsers(t *testing.T) {
	database := dbtest.New(t)
	mustInsertUser(t, database, "user-a")
	mustInsertUser(t, database, "user-b")
	repo := NewSQLiteLocationsRepository(database)
	ctx := context.Background()

	if err := repo.ReplaceAll(ctx, "user-a", denverBoulder()); err != nil {
		t.Fatalf("ReplaceAll user-a: %v", err)
	}
	if err := repo.ReplaceAll(ctx, "user-b", denverBoulder()[:1]); err != nil {
		t.Fatalf("ReplaceAll user-b: %v", err)
	}

	got, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != 3 {
		t.Errorf("Count = %d, want 3 (2 for user-a + 1 for user-b)", got)
	}
}
