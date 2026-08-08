package calendarsync

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/Prog-Strength/prog-strength-api/internal/db"
)

func newSyncDB(t *testing.T) *sql.DB {
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

var syncNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// seedActivity inserts a live activity owned by userID, created at createdAt.
func seedActivity(t *testing.T, conn *sql.DB, id, userID string, createdAt time.Time) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), `
		INSERT INTO activities (id, user_id, activity_type, ingest_source, source_activity_id,
			start_time, duration_seconds, tcx_s3_key, created_at, updated_at)
		VALUES (?, ?, 'running', 'manual_tcx', ?, ?, 1800, '', ?, ?)
	`, id, userID, id, createdAt, createdAt, createdAt)
	if err != nil {
		t.Fatalf("seed activity %s: %v", id, err)
	}
}

// seedConnection inserts a calendar connection for userID.
func seedConnection(t *testing.T, conn *sql.DB, userID, status string, connectedAt time.Time) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), `
		INSERT INTO user_calendar_connection (user_id, refresh_token_enc, refresh_token_nonce,
			google_calendar_id, scopes, status, connected_at, updated_at)
		VALUES (?, x'00', x'00', 'primary', 'calendar', ?, ?, ?)
	`, userID, status, connectedAt, connectedAt)
	if err != nil {
		t.Fatalf("seed connection %s: %v", userID, err)
	}
}

func TestActivitySync_GetMissingReturnsErrSyncStateNotFound(t *testing.T) {
	repo := NewSQLiteActivitySyncRepository(newSyncDB(t))

	_, err := repo.Get(context.Background(), "u1", "act_missing")
	if !errors.Is(err, ErrSyncStateNotFound) {
		t.Fatalf("Get err = %v, want ErrSyncStateNotFound", err)
	}
}

func TestActivitySync_MarkSyncedThenGet(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedActivity(t, conn, "act_1", "u1", syncNow)

	if err := repo.MarkSynced(context.Background(), "u1", "act_1", "gcal_evt_1", syncNow); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	got, err := repo.Get(context.Background(), "u1", "act_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != SyncSynced {
		t.Fatalf("Status = %q, want synced", got.Status)
	}
	if got.GoogleEventID == nil || *got.GoogleEventID != "gcal_evt_1" {
		t.Fatalf("GoogleEventID = %v, want gcal_evt_1", got.GoogleEventID)
	}
	if got.Attempts != 0 {
		t.Fatalf("Attempts = %d, want 0", got.Attempts)
	}
}

func TestActivitySync_MarkFailedIncrementsAttempts(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedActivity(t, conn, "act_1", "u1", syncNow)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := repo.MarkFailed(ctx, "u1", "act_1", nil, "boom", syncNow); err != nil {
			t.Fatalf("MarkFailed #%d: %v", i, err)
		}
		got, err := repo.Get(ctx, "u1", "act_1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Attempts != i {
			t.Fatalf("after %d failures Attempts = %d, want %d", i, got.Attempts, i)
		}
		if got.Status != SyncFailed {
			t.Fatalf("Status = %q, want failed", got.Status)
		}
	}
}

// A failed PATCH must not orphan a live Google event: passing a nil event id
// means "no new information", not "there is no event".
func TestActivitySync_MarkFailedPreservesAnExistingEventID(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedActivity(t, conn, "act_1", "u1", syncNow)
	ctx := context.Background()

	if err := repo.MarkSynced(ctx, "u1", "act_1", "gcal_evt_1", syncNow); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}
	if err := repo.MarkFailed(ctx, "u1", "act_1", nil, "transient 500", syncNow); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, _ := repo.Get(ctx, "u1", "act_1")
	if got.GoogleEventID == nil || *got.GoogleEventID != "gcal_evt_1" {
		t.Fatalf("GoogleEventID = %v, want the existing id preserved", got.GoogleEventID)
	}
}

// Recovering after failures must reset the counter, or the row stays one boot
// away from the reconciler's give-up cap forever.
func TestActivitySync_MarkSyncedResetsAttempts(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedActivity(t, conn, "act_1", "u1", syncNow)
	ctx := context.Background()

	_ = repo.MarkFailed(ctx, "u1", "act_1", nil, "boom", syncNow)
	_ = repo.MarkFailed(ctx, "u1", "act_1", nil, "boom", syncNow)
	if err := repo.MarkSynced(ctx, "u1", "act_1", "gcal_evt_1", syncNow); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	got, _ := repo.Get(ctx, "u1", "act_1")
	if got.Attempts != 0 {
		t.Fatalf("Attempts = %d, want 0 after recovery", got.Attempts)
	}
	if got.LastError != nil {
		t.Fatalf("LastError = %v, want cleared", *got.LastError)
	}
}

func TestActivitySync_ReleaseClearsTheEventID(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedActivity(t, conn, "act_1", "u1", syncNow)
	ctx := context.Background()

	_ = repo.MarkSynced(ctx, "u1", "act_1", "gcal_evt_1", syncNow)
	if err := repo.Release(ctx, "u1", "act_1", syncNow); err != nil {
		t.Fatalf("Release: %v", err)
	}

	got, _ := repo.Get(ctx, "u1", "act_1")
	if got.GoogleEventID != nil {
		t.Fatalf("GoogleEventID = %v, want nil", *got.GoogleEventID)
	}
	if got.Status != SyncPending {
		t.Fatalf("Status = %q, want pending", got.Status)
	}
}

// The no-backfill cutoff: created_at, not start_time.
func TestActivitySync_PendingSinceAppliesTheConnectedAtCutoff(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	connectedAt := syncNow

	seedConnection(t, conn, "u1", "connected", connectedAt)
	seedActivity(t, conn, "act_before", "u1", connectedAt.Add(-24*time.Hour))
	seedActivity(t, conn, "act_after", "u1", connectedAt.Add(time.Hour))

	got, err := repo.PendingSince(context.Background(), 5, 100)
	if err != nil {
		t.Fatalf("PendingSince: %v", err)
	}
	if len(got) != 1 || got[0].ActivityID != "act_after" {
		t.Fatalf("PendingSince = %+v, want only act_after", got)
	}
}

func TestActivitySync_PendingSinceSkipsRevokedConnections(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)

	seedConnection(t, conn, "u1", "revoked", syncNow)
	seedActivity(t, conn, "act_1", "u1", syncNow.Add(time.Hour))

	got, _ := repo.PendingSince(context.Background(), 5, 100)
	if len(got) != 0 {
		t.Fatalf("PendingSince = %+v, want none for a revoked connection", got)
	}
}

// A user who never connected must never appear, regardless of activity age.
func TestActivitySync_PendingSinceSkipsUsersWithNoConnection(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedActivity(t, conn, "act_1", "u_no_conn", syncNow)

	got, _ := repo.PendingSince(context.Background(), 5, 100)
	if len(got) != 0 {
		t.Fatalf("PendingSince = %+v, want none", got)
	}
}

func TestActivitySync_PendingSinceExcludesSyncedAndExhaustedRows(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	ctx := context.Background()
	seedConnection(t, conn, "u1", "connected", syncNow)
	for _, id := range []string{"act_synced", "act_exhausted", "act_retryable"} {
		seedActivity(t, conn, id, "u1", syncNow.Add(time.Hour))
	}

	_ = repo.MarkSynced(ctx, "u1", "act_synced", "gcal_evt_1", syncNow)
	for i := 0; i < 5; i++ {
		_ = repo.MarkFailed(ctx, "u1", "act_exhausted", nil, "boom", syncNow)
	}
	_ = repo.MarkFailed(ctx, "u1", "act_retryable", nil, "boom", syncNow)

	got, _ := repo.PendingSince(ctx, 5, 100)
	if len(got) != 1 || got[0].ActivityID != "act_retryable" {
		t.Fatalf("PendingSince = %+v, want only act_retryable", got)
	}
}

func TestActivitySync_PendingSinceSkipsSoftDeletedActivities(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedConnection(t, conn, "u1", "connected", syncNow)
	seedActivity(t, conn, "act_1", "u1", syncNow.Add(time.Hour))
	if _, err := conn.Exec(`UPDATE activities SET deleted_at = ? WHERE id = 'act_1'`, syncNow); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	got, _ := repo.PendingSince(context.Background(), 5, 100)
	if len(got) != 0 {
		t.Fatalf("PendingSince = %+v, want none for a deleted activity", got)
	}
}

// A backlog larger than the cap must drain oldest-first, so its tail is not
// starved by re-attempting the same head on every boot.
func TestActivitySync_PendingSinceIsOldestFirstAndRespectsTheLimit(t *testing.T) {
	conn := newSyncDB(t)
	repo := NewSQLiteActivitySyncRepository(conn)
	seedConnection(t, conn, "u1", "connected", syncNow)
	seedActivity(t, conn, "act_3", "u1", syncNow.Add(3*time.Hour))
	seedActivity(t, conn, "act_1", "u1", syncNow.Add(1*time.Hour))
	seedActivity(t, conn, "act_2", "u1", syncNow.Add(2*time.Hour))

	got, _ := repo.PendingSince(context.Background(), 5, 2)
	if len(got) != 2 {
		t.Fatalf("PendingSince returned %d rows, want the limit of 2", len(got))
	}
	if got[0].ActivityID != "act_1" || got[1].ActivityID != "act_2" {
		t.Fatalf("PendingSince = %+v, want act_1 then act_2", got)
	}
}
