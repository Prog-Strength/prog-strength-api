package activity

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
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

// newActivity builds a minimal valid Activity for the given owner /
// source / source-activity-id with two trackpoints. Defaults to a
// running activity since that's what the running-metrics path needs;
// callers swap ActivityType for cycling/walking tests.
func newActivity(userID string, source IngestSource, sourceActivityID string, start time.Time, dist float64, dur int) *Activity {
	avg := float64(dur) / (dist / 1000)
	return &Activity{
		UserID:           userID,
		ActivityType:     ActivityRunning,
		IngestSource:     source,
		SourceActivityID: sourceActivityID,
		StartTime:        start,
		Name:             ptrStr("Morning Run"),
		DistanceMeters:   dist,
		DurationSeconds:  dur,
		AvgPaceSecPerKm:  &avg,
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

func TestCreate_InsertsActivityTrackpointsAndArchives(t *testing.T) {
	t.Parallel()
	repo, arch := newRepo(t)
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	tcx := []byte("<TrainingCenterDatabase/>")
	if err := repo.Create(ctx, a, tcx); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID == "" {
		t.Fatal("expected generated ID")
	}
	// Hive-partitioned key in UTC.
	wantPrefix := "user_id=u1/activity_type=running/year=2026/month=06/day=01/"
	if !strings.HasPrefix(a.TCXS3Key, wantPrefix) {
		t.Fatalf("TCXS3Key = %q, want prefix %q", a.TCXS3Key, wantPrefix)
	}
	if !strings.HasSuffix(a.TCXS3Key, ".tcx") {
		t.Fatalf("TCXS3Key = %q, want .tcx suffix", a.TCXS3Key)
	}
	if a.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt set")
	}

	// Archiver received the exact bytes under the right key.
	got, ok := arch.Get(a.TCXS3Key)
	if !ok {
		t.Fatalf("archiver missing key %q", a.TCXS3Key)
	}
	if string(got) != string(tcx) {
		t.Fatalf("archived bytes = %q, want %q", got, tcx)
	}
	// The S3 object carries the ingest-source metadata stamp.
	meta, ok := arch.Meta(a.TCXS3Key)
	if !ok {
		t.Fatal("archiver missing metadata")
	}
	if meta.IngestSource != IngestManualTCX {
		t.Errorf("meta.IngestSource = %q, want %q", meta.IngestSource, IngestManualTCX)
	}

	// Trackpoints persisted and read back in order.
	loaded, err := repo.Get(ctx, "u1", a.ID)
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
	if loaded.ActivityType != ActivityRunning {
		t.Errorf("ActivityType = %q, want %q", loaded.ActivityType, ActivityRunning)
	}
	if loaded.IngestSource != IngestManualTCX {
		t.Errorf("IngestSource = %q, want %q", loaded.IngestSource, IngestManualTCX)
	}
}

func TestCreate_DuplicatePerSource(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a1 := newActivity("u1", IngestManualTCX, "dup", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a1, []byte("a")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	a2 := newActivity("u1", IngestManualTCX, "dup", mustTime(t, "2026-06-02T07:00:00Z"), 6000, 1800)
	if err := repo.Create(ctx, a2, []byte("b")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}

	// Same source_activity_id for a different USER is allowed.
	a3 := newActivity("u2", IngestManualTCX, "dup", mustTime(t, "2026-06-02T07:00:00Z"), 6000, 1800)
	if err := repo.Create(ctx, a3, []byte("c")); err != nil {
		t.Fatalf("cross-user same source id should succeed: %v", err)
	}

	// Same source_activity_id from a different INGEST SOURCE is allowed:
	// a future Garmin Connect sync of an activity that the user already
	// uploaded via manual TCX is a separate record by design.
	a4 := newActivity("u1", IngestGarminAPI, "dup", mustTime(t, "2026-06-04T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a4, []byte("d")); err != nil {
		t.Fatalf("cross-source same activity id should succeed: %v", err)
	}
}

func TestCreate_ReimportAfterSoftDelete(t *testing.T) {
	// The dedup constraint should only fire on LIVE rows. A user who
	// deletes an activity and then re-imports the same TCX (e.g. to pick
	// up an algorithm change in the summarizer) must be able to do so —
	// the soft-deleted row is preserved for audit but no longer blocks
	// the activity slot.
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a1 := newActivity("u1", IngestManualTCX, "reimport", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a1, []byte("a")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", a1.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	a2 := newActivity("u1", IngestManualTCX, "reimport", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a2, []byte("b")); err != nil {
		t.Fatalf("re-import after soft-delete should succeed, got %v", err)
	}
	if a2.ID == "" || a2.ID == a1.ID {
		t.Errorf("re-imported activity should get a fresh ID; got a1=%q a2=%q", a1.ID, a2.ID)
	}

	a3 := newActivity("u1", IngestManualTCX, "reimport", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a3, []byte("c")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("re-import while a live row exists must still 409, got %v", err)
	}
}

func TestCreate_ArchiverFailureRollsBack(t *testing.T) {
	t.Parallel()
	repo, arch := newRepo(t)
	ctx := context.Background()
	arch.PutErr = errors.New("s3 down")

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("x")); !errors.Is(err, ErrStorage) {
		t.Fatalf("want ErrStorage, got %v", err)
	}

	// No row and no trackpoints should have persisted (transaction rolled back).
	var activities, points int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM activities`).Scan(&activities); err != nil {
		t.Fatalf("count activities: %v", err)
	}
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM activity_trackpoints`).Scan(&points); err != nil {
		t.Fatalf("count trackpoints: %v", err)
	}
	if activities != 0 || points != 0 {
		t.Fatalf("rollback failed: activities=%d points=%d", activities, points)
	}
	if arch.Len() != 0 {
		t.Fatalf("archiver should hold nothing, got %d", arch.Len())
	}
}

func TestList_NewestFirstBeforeAndSoftDelete(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	older := newActivity("u1", IngestManualTCX, "g-old", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	newer := newActivity("u1", IngestManualTCX, "g-new", mustTime(t, "2026-06-03T07:00:00Z"), 6000, 1800)
	deleted := newActivity("u1", IngestManualTCX, "g-del", mustTime(t, "2026-06-02T07:00:00Z"), 4000, 1200)
	for _, a := range []*Activity{older, newer, deleted} {
		if err := repo.Create(ctx, a, []byte("x")); err != nil {
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
		t.Fatalf("want 2 live activities, got %d", len(got))
	}
	if got[0].ID != newer.ID || got[1].ID != older.ID {
		t.Fatalf("wrong order: %s, %s", got[0].ID, got[1].ID)
	}
	if got[0].Trackpoints != nil {
		t.Fatal("List should not load trackpoints")
	}

	before := mustTime(t, "2026-06-02T00:00:00Z")
	got, err = repo.List(ctx, "u1", 10, &before)
	if err != nil {
		t.Fatalf("List before: %v", err)
	}
	if len(got) != 1 || got[0].ID != older.ID {
		t.Fatalf("before cursor wrong: %+v", got)
	}

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

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := repo.Get(ctx, "u2", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Get: want ErrNotFound, got %v", err)
	}
	if _, err := repo.Get(ctx, "u1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Get: want ErrNotFound, got %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", a.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := repo.Get(ctx, "u1", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted Get: want ErrNotFound, got %v", err)
	}
}

func TestGetBySourceActivityID(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "g-find", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetBySourceActivityID(ctx, "u1", IngestManualTCX, "g-find")
	if err != nil {
		t.Fatalf("GetBySourceActivityID: %v", err)
	}
	if got.ID != a.ID {
		t.Fatalf("got id %s, want %s", got.ID, a.ID)
	}
	if _, err := repo.GetBySourceActivityID(ctx, "u1", IngestManualTCX, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: want ErrNotFound, got %v", err)
	}
	// Wrong source must not find the row.
	if _, err := repo.GetBySourceActivityID(ctx, "u1", IngestGarminAPI, "g-find"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong source: want ErrNotFound, got %v", err)
	}
}

func TestRename(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Rename(ctx, "u1", a.ID, "Tempo Run")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got.Name == nil || *got.Name != "Tempo Run" {
		t.Fatalf("name not updated: %+v", got.Name)
	}
	if _, err := repo.Rename(ctx, "u2", a.ID, "Hax"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Rename: want ErrNotFound, got %v", err)
	}
}

func TestSoftDelete_ThenGetNotFound(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "g1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, a, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", a.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if err := repo.SoftDelete(ctx, "u1", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}
	if _, err := repo.Get(ctx, "u1", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: want ErrNotFound, got %v", err)
	}
}

func TestRunningMetrics(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	// now: Wednesday 2026-06-10 12:00 local. Local week = Mon 6/8 .. Mon 6/15.
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, loc)

	wk := newActivity("u1", IngestManualTCX, "wk", time.Date(2026, 6, 9, 7, 0, 0, 0, loc), 10000, 3000)
	prior := newActivity("u1", IngestManualTCX, "prior", time.Date(2026, 6, 5, 7, 0, 0, 0, loc), 5000, 1500)
	earlyMonth := newActivity("u1", IngestManualTCX, "em", time.Date(2026, 6, 2, 7, 0, 0, 0, loc), 4000, 1200)
	other := newActivity("u2", IngestManualTCX, "other", time.Date(2026, 6, 9, 7, 0, 0, 0, loc), 99000, 9000)
	// A walk in u1's current week must NOT contribute to running metrics.
	walk := newActivity("u1", IngestManualTCX, "wlk", time.Date(2026, 6, 11, 7, 0, 0, 0, loc), 50000, 6000)
	walk.ActivityType = ActivityWalking
	for _, a := range []*Activity{wk, prior, earlyMonth, other, walk} {
		if err = repo.Create(ctx, a, []byte("x")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	m, err := repo.RunningMetrics(ctx, "u1", now, loc)
	if err != nil {
		t.Fatalf("RunningMetrics: %v", err)
	}
	// Only the one running activity contributes; the walk is excluded.
	if m.CurrentWeek.RunCount != 1 || m.CurrentWeek.DistanceMeters != 10000 {
		t.Fatalf("current week wrong (walks must not contribute): %+v", m.CurrentWeek)
	}
	if m.DeltaPctVsPriorWeek == nil || math.Abs(*m.DeltaPctVsPriorWeek-11.1111) > 0.01 {
		t.Fatalf("delta wrong: %v", deref(m.DeltaPctVsPriorWeek))
	}
	if m.CurrentMonth.RunCount != 3 || m.CurrentMonth.DistanceMeters != 19000 {
		t.Fatalf("current month wrong (running only): %+v", m.CurrentMonth)
	}
	if m.AllTime.RunCount != 3 || m.AllTime.DistanceMeters != 19000 {
		t.Fatalf("all time wrong (running only): %+v", m.AllTime)
	}
	if m.RecentAvgPaceSecPerKm == nil || *m.RecentAvgPaceSecPerKm != 300 {
		t.Fatalf("recent pace wrong: %v", m.RecentAvgPaceSecPerKm)
	}
}
