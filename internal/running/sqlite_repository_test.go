package running

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	_ "github.com/mattn/go-sqlite3"
)

// newMigratedDB opens a fresh migrated database in a temp dir with
// foreign keys on, mirroring internal/db/migrate_test.go. Each test gets
// its own file so they run in parallel without sharing schema state.
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

func deref(f *float64) float64 {
	if f == nil {
		return -1
	}
	return *f
}

func ptrStr(s string) *string { return &s }
func ptrInt(i int) *int       { return &i }
func ptrF(f float64) *float64 { return &f }

// newSession builds a minimal valid Session for the given owner/garmin id
// with two trackpoints. Optional overrides tweak fields tests care about.
func newSession(userID, garminID string, start time.Time, dist float64, dur int) *Session {
	return &Session{
		UserID:           userID,
		GarminActivityID: garminID,
		StartTime:        start,
		Name:             ptrStr("Morning Run"),
		DistanceMeters:   dist,
		DurationSeconds:  dur,
		AvgPaceSecPerKm:  float64(dur) / (dist / 1000),
		BestPaceSecPerKm: ptrF(280),
		AvgHeartRateBpm:  ptrInt(150),
		MaxHeartRateBpm:  ptrInt(175),
		TotalCalories:    ptrInt(400),
		Trackpoints: []Trackpoint{
			{Sequence: 0, ElapsedSeconds: 0, DistanceMeters: 0, HeartRateBpm: ptrInt(140)},
			{Sequence: 1, ElapsedSeconds: 10, DistanceMeters: 30, HeartRateBpm: ptrInt(150)},
		},
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt
}

func newRepo(t *testing.T) (*SQLiteRepository, *MemoryArchiver) {
	t.Helper()
	arch := NewMemoryArchiver()
	return NewSQLiteRepository(newMigratedDB(t), arch), arch
}

func TestCreate_InsertsSessionTrackpointsAndArchives(t *testing.T) {
	t.Parallel()
	repo, arch := newRepo(t)
	ctx := context.Background()

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	tcx := []byte("<TrainingCenterDatabase/>")
	if err := repo.Create(ctx, s, tcx); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID == "" {
		t.Fatal("expected generated ID")
	}
	wantKey := "runs/u1/" + s.ID + ".tcx"
	if s.TCXS3Key != wantKey {
		t.Fatalf("TCXS3Key = %q, want %q", s.TCXS3Key, wantKey)
	}
	if s.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt set")
	}

	// Archiver received the exact bytes under the right key.
	got, ok := arch.Get(wantKey)
	if !ok {
		t.Fatalf("archiver missing key %q", wantKey)
	}
	if string(got) != string(tcx) {
		t.Fatalf("archived bytes = %q, want %q", got, tcx)
	}

	// Trackpoints persisted and read back in order.
	loaded, err := repo.Get(ctx, "u1", s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Trackpoints) != 2 {
		t.Fatalf("want 2 trackpoints, got %d", len(loaded.Trackpoints))
	}
	if loaded.Trackpoints[0].Sequence != 0 || loaded.Trackpoints[1].Sequence != 1 {
		t.Fatalf("trackpoints out of order: %+v", loaded.Trackpoints)
	}
	if loaded.Name == nil || *loaded.Name != "Morning Run" {
		t.Fatalf("name not persisted: %+v", loaded.Name)
	}
}

func TestCreate_DuplicateGarminActivity(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	s1 := newSession("u1", "dup", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s1, []byte("a")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	s2 := newSession("u1", "dup", mustTime(t, "2026-06-02T07:00:00Z"), 6000, 1800)
	if err := repo.Create(ctx, s2, []byte("b")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}

	// Same garmin id for a different user is allowed.
	s3 := newSession("u2", "dup", mustTime(t, "2026-06-02T07:00:00Z"), 6000, 1800)
	if err := repo.Create(ctx, s3, []byte("c")); err != nil {
		t.Fatalf("cross-user same garmin id should succeed: %v", err)
	}
}

func TestCreate_ReimportAfterSoftDelete(t *testing.T) {
	// The dedup constraint should only fire on LIVE rows. A user who
	// deletes a run and then re-imports the same TCX (e.g. to pick up an
	// algorithm change in the summarizer) must be able to do so — the
	// soft-deleted row is preserved for audit but no longer blocks the
	// activity slot.
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	s1 := newSession("u1", "reimport", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s1, []byte("a")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", s1.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	s2 := newSession("u1", "reimport", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s2, []byte("b")); err != nil {
		t.Fatalf("re-import after soft-delete should succeed, got %v", err)
	}
	if s2.ID == "" || s2.ID == s1.ID {
		t.Errorf("re-imported session should get a fresh ID; got s1=%q s2=%q", s1.ID, s2.ID)
	}

	// A second re-import while s2 is still live should still 409 — the
	// constraint is "no two LIVE rows share an activity ID," not "any
	// row can be re-imported any time."
	s3 := newSession("u1", "reimport", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s3, []byte("c")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("re-import while a live row exists must still 409, got %v", err)
	}
}

func TestCreate_ArchiverFailureRollsBack(t *testing.T) {
	t.Parallel()
	repo, arch := newRepo(t)
	ctx := context.Background()
	arch.PutErr = errors.New("s3 down")

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("x")); !errors.Is(err, ErrStorage) {
		t.Fatalf("want ErrStorage, got %v", err)
	}

	// No row and no trackpoints should have persisted (transaction rolled back).
	var sessions, points int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM running_sessions`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM running_trackpoints`).Scan(&points); err != nil {
		t.Fatalf("count trackpoints: %v", err)
	}
	if sessions != 0 || points != 0 {
		t.Fatalf("rollback failed: sessions=%d points=%d", sessions, points)
	}
	if arch.Len() != 0 {
		t.Fatalf("archiver should hold nothing, got %d", arch.Len())
	}
}

func TestList_NewestFirstBeforeAndSoftDelete(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	older := newSession("u1", "g-old", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	newer := newSession("u1", "g-new", mustTime(t, "2026-06-03T07:00:00Z"), 6000, 1800)
	deleted := newSession("u1", "g-del", mustTime(t, "2026-06-02T07:00:00Z"), 4000, 1200)
	for _, s := range []*Session{older, newer, deleted} {
		if err := repo.Create(ctx, s, []byte("x")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if err := repo.SoftDelete(ctx, "u1", deleted.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	got, err := repo.List(ctx, "u1", 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 live sessions, got %d", len(got))
	}
	// Newest first.
	if got[0].ID != newer.ID || got[1].ID != older.ID {
		t.Fatalf("wrong order: %s, %s", got[0].ID, got[1].ID)
	}
	// List never ships trackpoints.
	if got[0].Trackpoints != nil {
		t.Fatal("List should not load trackpoints")
	}

	// before-cursor excludes sessions at/after the cursor.
	before := mustTime(t, "2026-06-02T00:00:00Z")
	got, err = repo.List(ctx, "u1", 10, &before)
	if err != nil {
		t.Fatalf("List before: %v", err)
	}
	if len(got) != 1 || got[0].ID != older.ID {
		t.Fatalf("before cursor wrong: %+v", got)
	}

	// limit caps results.
	got, err = repo.List(ctx, "u1", 1, nil)
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	if len(got) != 1 || got[0].ID != newer.ID {
		t.Fatalf("limit wrong: %+v", got)
	}
}

func TestGet_NotFoundCases(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Another user can't read it.
	if _, err := repo.Get(ctx, "u2", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Get: want ErrNotFound, got %v", err)
	}
	// Missing id.
	if _, err := repo.Get(ctx, "u1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Get: want ErrNotFound, got %v", err)
	}
	// Soft-deleted is invisible.
	if err := repo.SoftDelete(ctx, "u1", s.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := repo.Get(ctx, "u1", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted Get: want ErrNotFound, got %v", err)
	}
}

func TestGetByGarminActivityID(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	s := newSession("u1", "g-find", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByGarminActivityID(ctx, "u1", "g-find")
	if err != nil {
		t.Fatalf("GetByGarminActivityID: %v", err)
	}
	if got.ID != s.ID {
		t.Fatalf("got id %s, want %s", got.ID, s.ID)
	}
	if _, err := repo.GetByGarminActivityID(ctx, "u1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: want ErrNotFound, got %v", err)
	}
}

func TestRename(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Rename(ctx, "u1", s.ID, "Tempo Run")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got.Name == nil || *got.Name != "Tempo Run" {
		t.Fatalf("name not updated: %+v", got.Name)
	}
	// Ownership isolation.
	if _, err := repo.Rename(ctx, "u2", s.ID, "Hax"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Rename: want ErrNotFound, got %v", err)
	}
}

func TestSoftDelete_ThenGetNotFound(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	s := newSession("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, s, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", s.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// Second delete finds no live row.
	if err := repo.SoftDelete(ctx, "u1", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}
	if _, err := repo.Get(ctx, "u1", s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: want ErrNotFound, got %v", err)
	}
}

func TestMetrics(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	// now: Wednesday 2026-06-10 12:00 local. Local week = Mon 6/8 .. Mon 6/15.
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, loc)

	// This-week run.
	wk := newSession("u1", "wk", time.Date(2026, 6, 9, 7, 0, 0, 0, loc), 10000, 3000)
	// Prior-week run (Mon 6/1 .. Mon 6/8): Friday 6/5.
	prior := newSession("u1", "prior", time.Date(2026, 6, 5, 7, 0, 0, 0, loc), 5000, 1500)
	// Earlier-this-month but outside the week (6/2), also feeds month total.
	earlyMonth := newSession("u1", "em", time.Date(2026, 6, 2, 7, 0, 0, 0, loc), 4000, 1200)
	// A different user's run must not leak in.
	other := newSession("u2", "other", time.Date(2026, 6, 9, 7, 0, 0, 0, loc), 99000, 9000)
	for _, s := range []*Session{wk, prior, earlyMonth, other} {
		if err := repo.Create(ctx, s, []byte("x")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	m, err := repo.Metrics(ctx, "u1", now, loc)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if m.CurrentWeek.RunCount != 1 || m.CurrentWeek.DistanceMeters != 10000 {
		t.Fatalf("current week wrong: %+v", m.CurrentWeek)
	}
	// Prior local week is Mon 6/1 .. Mon 6/8, which contains BOTH the
	// 6/5 run (5000) and the 6/2 run (4000) = 9000.
	// delta = (10000-9000)/9000*100 = 11.11%.
	if m.DeltaPctVsPriorWeek == nil || math.Abs(*m.DeltaPctVsPriorWeek-11.1111) > 0.01 {
		t.Fatalf("delta wrong: %v", deref(m.DeltaPctVsPriorWeek))
	}
	// month: wk + prior + earlyMonth all in June = 19000, 3 runs.
	if m.CurrentMonth.RunCount != 3 || m.CurrentMonth.DistanceMeters != 19000 {
		t.Fatalf("current month wrong: %+v", m.CurrentMonth)
	}
	// all time u1: 19000 / 3.
	if m.AllTime.RunCount != 3 || m.AllTime.DistanceMeters != 19000 {
		t.Fatalf("all time wrong: %+v", m.AllTime)
	}
	// recent pace (last 30d covers all three): sum(dur)=5700, sum(dist)=19000m.
	// pace = 5700 / (19000/1000) = 300 sec/km.
	if m.RecentAvgPaceSecPerKm == nil || *m.RecentAvgPaceSecPerKm != 300 {
		t.Fatalf("recent pace wrong: %v", m.RecentAvgPaceSecPerKm)
	}
}
