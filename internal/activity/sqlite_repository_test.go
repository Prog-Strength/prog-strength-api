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
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/hrzones"
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
	got, err := arch.Get(context.Background(), a.TCXS3Key)
	if err != nil {
		t.Fatalf("archiver missing key %q: %v", a.TCXS3Key, err)
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

// TestCalibrate_UniformScale asserts the header distance and every trackpoint
// distance scale by the same factor, avg pace recomputes from the new
// distance, best pace scales by 1/f, and raw_distance is left untouched.
func TestCalibrate_UniformScale(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a := newActivity("u1", IngestManualTCX, "cal1", mustTime(t, "2026-06-01T07:00:00Z"), 5000, 1500)
	a.Environment = EnvironmentIndoor
	a.RawDistanceMeters = 5000
	bestPace := 280.0
	a.BestPaceSecPerKm = &bestPace
	pace := 300.0
	a.Trackpoints = []Trackpoint{
		{Sequence: 0, ElapsedSeconds: 0, DistanceMeters: 0},
		{Sequence: 1, ElapsedSeconds: 750, DistanceMeters: 2500, PaceSecPerKm: &pace},
		{Sequence: 2, ElapsedSeconds: 1500, DistanceMeters: 5000, PaceSecPerKm: &pace},
	}
	if err := repo.Create(ctx, a, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	newDist := 4500.0
	f := newDist / 5000.0
	got, err := repo.Calibrate(ctx, "u1", a.ID, newDist)
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if math.Abs(got.DistanceMeters-newDist) > 0.001 {
		t.Errorf("distance = %.4f, want %.4f", got.DistanceMeters, newDist)
	}
	if math.Abs(got.RawDistanceMeters-5000) > 0.001 {
		t.Errorf("raw_distance = %.4f, want 5000 (untouched)", got.RawDistanceMeters)
	}
	wantAvg := 1500.0 / (newDist / 1000)
	if got.AvgPaceSecPerKm == nil || math.Abs(*got.AvgPaceSecPerKm-wantAvg) > 0.001 {
		t.Errorf("avg pace = %v, want %.4f", got.AvgPaceSecPerKm, wantAvg)
	}
	if got.BestPaceSecPerKm == nil || math.Abs(*got.BestPaceSecPerKm-(280.0/f)) > 0.001 {
		t.Errorf("best pace = %v, want %.4f", got.BestPaceSecPerKm, 280.0/f)
	}
	// Trackpoints scaled uniformly; the last cumulative distance == new total.
	last := got.Trackpoints[len(got.Trackpoints)-1]
	if math.Abs(last.DistanceMeters-newDist) > 0.001 {
		t.Errorf("last trackpoint distance = %.4f, want %.4f", last.DistanceMeters, newDist)
	}
	mid := got.Trackpoints[1]
	if math.Abs(mid.DistanceMeters-2500*f) > 0.001 {
		t.Errorf("mid trackpoint distance = %.4f, want %.4f", mid.DistanceMeters, 2500*f)
	}
	if mid.PaceSecPerKm == nil || math.Abs(*mid.PaceSecPerKm-(300.0/f)) > 0.001 {
		t.Errorf("mid trackpoint pace = %v, want %.4f", mid.PaceSecPerKm, 300.0/f)
	}

	if _, err := repo.Calibrate(ctx, "u2", a.ID, newDist); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Calibrate: want ErrNotFound, got %v", err)
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

// TestListRunningSamplesSince_Filters verifies the projection returns only
// running activities, excluding walks, soft-deleted rows, before-since rows,
// and other users' rows.
func TestListRunningSamplesSince_Filters(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	since := mustTime(t, "2026-05-01T00:00:00Z")

	run := newActivity("u1", IngestManualTCX, "run", mustTime(t, "2026-05-10T07:00:00Z"), 5000, 1500)

	walk := newActivity("u1", IngestManualTCX, "walk", mustTime(t, "2026-05-11T07:00:00Z"), 6000, 1800)
	walk.ActivityType = ActivityWalking

	beforeSince := newActivity("u1", IngestManualTCX, "old", mustTime(t, "2026-04-20T07:00:00Z"), 4000, 1200)

	deleted := newActivity("u1", IngestManualTCX, "del", mustTime(t, "2026-05-12T07:00:00Z"), 7000, 2100)

	other := newActivity("u2", IngestManualTCX, "other", mustTime(t, "2026-05-13T07:00:00Z"), 8000, 2400)

	for _, a := range []*Activity{run, walk, beforeSince, deleted, other} {
		if err := repo.Create(ctx, a, []byte("x")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if err := repo.SoftDelete(ctx, "u1", deleted.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	got, err := repo.ListRunningSamplesSince(ctx, "u1", since)
	if err != nil {
		t.Fatalf("ListRunningSamplesSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d samples, want 1: %+v", len(got), got)
	}
	if got[0].DistanceMeters != 5000 {
		t.Fatalf("distance = %v, want 5000", got[0].DistanceMeters)
	}
	if !got[0].StartTime.Equal(run.StartTime) {
		t.Fatalf("start = %v, want %v", got[0].StartTime, run.StartTime)
	}
}

// --- Running best efforts ----------------------------------------------

// newActivityWithEfforts builds a running activity carrying the given
// best-effort rows, for the persistence + read-query tests.
func newActivityWithEfforts(userID, source string, start time.Time, efforts []ActivityBestEffort) *Activity {
	a := newActivity(userID, IngestManualTCX, source, start, 10000, 3000)
	a.BestEfforts = efforts
	return a
}

// TestCreate_PersistsBestEfforts asserts the best-effort rows written in
// Create's transaction land in activity_best_efforts.
func TestCreate_PersistsBestEfforts(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a := newActivityWithEfforts("u1", "g1", mustTime(t, "2026-06-01T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "1mi", DurationSeconds: 386.2},
		{DistanceKey: "5k", DurationSeconds: 1184.7},
	})
	if err := repo.Create(ctx, a, []byte("<x/>")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var count int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM activity_best_efforts WHERE activity_id = ?`, a.ID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("want 2 best-effort rows, got %d", count)
	}
}

// TestGetUserRunningBestEfforts_PerDistanceMin asserts the read query
// returns the fastest window per distance with the correct activity, that
// duration ties resolve to the earliest start_time, and that a walk's best
// efforts never appear in the running query.
func TestGetUserRunningBestEfforts_PerDistanceMin(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	// Two running activities. The later one has the faster 5K; the earlier
	// one has the faster 1mi.
	early := newActivityWithEfforts("u1", "g-early", mustTime(t, "2026-04-01T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "1mi", DurationSeconds: 380},
		{DistanceKey: "5k", DurationSeconds: 1300},
	})
	if err := repo.Create(ctx, early, []byte("<x/>")); err != nil {
		t.Fatalf("Create early: %v", err)
	}
	late := newActivityWithEfforts("u1", "g-late", mustTime(t, "2026-05-01T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "1mi", DurationSeconds: 400},
		{DistanceKey: "5k", DurationSeconds: 1184.7},
	})
	if err := repo.Create(ctx, late, []byte("<x/>")); err != nil {
		t.Fatalf("Create late: %v", err)
	}

	// A walk that would, if counted, hold the fastest 5K. It must be excluded.
	walk := newActivity("u1", IngestManualTCX, "g-walk", mustTime(t, "2026-05-10T07:00:00Z"), 10000, 3000)
	walk.ActivityType = ActivityWalking
	walk.BestEfforts = []ActivityBestEffort{{DistanceKey: "5k", DurationSeconds: 100}}
	if err := repo.Create(ctx, walk, []byte("<x/>")); err != nil {
		t.Fatalf("Create walk: %v", err)
	}

	bests, err := repo.GetUserRunningBestEfforts(ctx, "u1")
	if err != nil {
		t.Fatalf("GetUserRunningBestEfforts: %v", err)
	}
	byKey := map[string]RunningBestEffort{}
	for _, b := range bests {
		byKey[b.DistanceKey] = b
	}

	if got := byKey["1mi"]; got.DurationSeconds != 380 || got.ActivityID != early.ID {
		t.Errorf("1mi best = %+v, want 380 from %s", got, early.ID)
	}
	if got := byKey["5k"]; got.DurationSeconds != 1184.7 || got.ActivityID != late.ID {
		t.Errorf("5k best = %+v, want 1184.7 from %s (not the walk)", got, late.ID)
	}
}

// TestGetUserRunningBestEfforts_TieBreakEarliest asserts that two
// activities tied on duration at a distance resolve to the earliest start.
func TestGetUserRunningBestEfforts_TieBreakEarliest(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	first := newActivityWithEfforts("u1", "g-first", mustTime(t, "2026-03-01T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1200},
	})
	if err := repo.Create(ctx, first, []byte("<x/>")); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second := newActivityWithEfforts("u1", "g-second", mustTime(t, "2026-03-15T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1200},
	})
	if err := repo.Create(ctx, second, []byte("<x/>")); err != nil {
		t.Fatalf("Create second: %v", err)
	}

	bests, err := repo.GetUserRunningBestEfforts(ctx, "u1")
	if err != nil {
		t.Fatalf("GetUserRunningBestEfforts: %v", err)
	}
	if len(bests) != 1 {
		t.Fatalf("want 1 best, got %d", len(bests))
	}
	if bests[0].ActivityID != first.ID {
		t.Errorf("tie winner = %s, want the earliest-start %s", bests[0].ActivityID, first.ID)
	}
}

// TestGetRunningBestEffortHistory_Ascending asserts the history query
// returns every point at a distance ordered by start_time ascending.
func TestGetRunningBestEffortHistory_Ascending(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	mid := newActivityWithEfforts("u1", "g-mid", mustTime(t, "2026-02-01T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1312.7},
	})
	earliest := newActivityWithEfforts("u1", "g-earliest", mustTime(t, "2026-01-01T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1340.2},
	})
	latest := newActivityWithEfforts("u1", "g-latest", mustTime(t, "2026-03-01T07:00:00Z"), []ActivityBestEffort{
		{DistanceKey: "5k", DurationSeconds: 1184.7},
	})
	// Insert out of order to prove the query sorts.
	for _, a := range []*Activity{mid, earliest, latest} {
		if err := repo.Create(ctx, a, []byte("<x/>")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	pts, err := repo.GetRunningBestEffortHistory(ctx, "u1", "5k")
	if err != nil {
		t.Fatalf("GetRunningBestEffortHistory: %v", err)
	}
	if len(pts) != 3 {
		t.Fatalf("want 3 points, got %d", len(pts))
	}
	for i := 1; i < len(pts); i++ {
		if pts[i].ActivityStartTime.Before(pts[i-1].ActivityStartTime) {
			t.Errorf("points not ascending by start_time: %+v", pts)
		}
	}
	if pts[0].ActivityID != earliest.ID || pts[2].ActivityID != latest.ID {
		t.Errorf("order wrong: first=%s last=%s", pts[0].ActivityID, pts[2].ActivityID)
	}
}

// hrActivity is newActivity with an explicit trackpoint set so the test can
// control which HR samples a run contributes (newActivity hardcodes 140/150).
func hrActivity(userID, sourceID string, start time.Time, hrSamples []int) *Activity {
	a := newActivity(userID, IngestManualTCX, sourceID, start, 5000, 1500)
	pts := make([]Trackpoint, len(hrSamples))
	for i, hr := range hrSamples {
		pts[i] = Trackpoint{
			Sequence:       i,
			ElapsedSeconds: i * 10,
			DistanceMeters: float64(i * 30),
			HeartRateBpm:   ptrInt(hr),
		}
	}
	a.Trackpoints = pts
	return a
}

func TestRecentHRStats(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	now := time.Now()
	window := 90 * 24 * time.Hour

	// A and B are in-window HR-bearing runs (the only two that should count).
	aSamples := []int{140, 150, 160, 170}
	bSamples := []int{145, 155, 165, 175, 185}
	a := hrActivity("u1", "a", now.Add(-10*24*time.Hour), aSamples)
	b := hrActivity("u1", "b", now.Add(-20*24*time.Hour), bSamples)
	// C is out of the window (older than now-window).
	c := hrActivity("u1", "c", now.Add(-200*24*time.Hour), []int{200, 205})
	// D is in-window with HR but soft-deleted → excluded.
	d := hrActivity("u1", "d", now.Add(-5*24*time.Hour), []int{210, 215})
	// X is the "current" run, passed as excludeActivityID → excluded.
	x := hrActivity("u1", "x", now.Add(-1*24*time.Hour), []int{120, 130})
	// A non-running in-window activity → excluded by the activity_type filter.
	walk := hrActivity("u1", "walk", now.Add(-3*24*time.Hour), []int{100, 110})
	walk.ActivityType = ActivityWalking

	for _, act := range []*Activity{a, b, c, d, x, walk} {
		if err := repo.Create(ctx, act, []byte("<x/>")); err != nil {
			t.Fatalf("Create %s: %v", act.SourceActivityID, err)
		}
	}
	if err := repo.SoftDelete(ctx, "u1", d.ID); err != nil {
		t.Fatalf("SoftDelete D: %v", err)
	}

	stats, err := repo.RecentHRStats(ctx, "u1", window, x.ID)
	if err != nil {
		t.Fatalf("RecentHRStats: %v", err)
	}

	if stats.HistoryRunCount != 2 {
		t.Errorf("HistoryRunCount = %d, want 2 (A, B)", stats.HistoryRunCount)
	}
	want := hrzones.P99(append(append([]int{}, aSamples...), bSamples...))
	if stats.RecentHRSamplesP99 == nil || want == nil || *stats.RecentHRSamplesP99 != *want {
		t.Errorf("RecentHRSamplesP99 = %v, want %v", stats.RecentHRSamplesP99, want)
	}
	if stats.CurrentRunP99 != nil {
		t.Errorf("CurrentRunP99 = %v, want nil (handler fills it)", stats.CurrentRunP99)
	}
}

func TestRecentHRStats_NoQualifyingRuns(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	now := time.Now()
	window := 90 * 24 * time.Hour

	// Only an out-of-window run exists for u1; nothing qualifies.
	old := hrActivity("u1", "old", now.Add(-200*24*time.Hour), []int{150, 160})
	if err := repo.Create(ctx, old, []byte("<x/>")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stats, err := repo.RecentHRStats(ctx, "u1", window, "")
	if err != nil {
		t.Fatalf("RecentHRStats: %v", err)
	}
	if stats.HistoryRunCount != 0 {
		t.Errorf("HistoryRunCount = %d, want 0", stats.HistoryRunCount)
	}
	if stats.RecentHRSamplesP99 != nil {
		t.Errorf("RecentHRSamplesP99 = %v, want nil", stats.RecentHRSamplesP99)
	}
	if stats.CurrentRunP99 != nil {
		t.Errorf("CurrentRunP99 = %v, want nil", stats.CurrentRunP99)
	}
}
