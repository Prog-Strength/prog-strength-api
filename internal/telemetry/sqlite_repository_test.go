package telemetry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// newTestTelemetryRepo opens a fresh telemetry.db in a t.TempDir(),
// runs all telemetry_migrations, and returns the *SQLiteRepository
// plus a cleanup func. Each test gets its own database file so they
// can run in parallel without sharing schema state.
func newTestTelemetryRepo(t *testing.T) (*SQLiteRepository, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open telemetry db: %v", err)
	}
	if err := db.MigrateTelemetry(conn); err != nil {
		conn.Close()
		t.Fatalf("migrate telemetry: %v", err)
	}
	return NewSQLiteRepository(conn), func() { _ = conn.Close() }
}

func TestInsertTurn_PersistsIntentFields(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()

	want := AgentTurn{
		ID:                       "turn-int-1",
		UserID:                   "u-1",
		SessionID:                "s-1",
		Model:                    "claude-haiku-4-5-20251001",
		RoutedTier:               "simple",
		RouterModel:              "claude-haiku-4-5-20251001",
		Intent:                   "log_nutrition",
		IntentPrefetchDurationMs: 87,
		IntentPrefetchFailed:     false,
		HadImage:                 true,
		CompletionReason:         "end_turn",
		StartedAt:                time.Now().UTC(),
		EndedAt:                  time.Now().UTC(),
		CreatedAt:                time.Now().UTC(),
	}
	if err := repo.InsertTurn(context.Background(), want); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var (
		gotIntent           string
		gotPrefetchDuration int
		gotPrefetchFailed   int
		gotHadImage         int
	)
	err := repo.db.QueryRow(
		`SELECT intent, intent_prefetch_duration_ms, intent_prefetch_failed, had_image
		   FROM agent_turns WHERE id = ?`, want.ID,
	).Scan(&gotIntent, &gotPrefetchDuration, &gotPrefetchFailed, &gotHadImage)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotIntent != "log_nutrition" || gotPrefetchDuration != 87 || gotPrefetchFailed != 0 || gotHadImage != 1 {
		t.Fatalf("got intent=%q prefetch=%d failed=%d had_image=%d", gotIntent, gotPrefetchDuration, gotPrefetchFailed, gotHadImage)
	}
}

// TestInsertTurn_HadImageDefaultsFalse confirms the Go zero value for
// HadImage persists as 0 (the default-false path for non-image turns).
func TestInsertTurn_HadImageDefaultsFalse(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()

	want := AgentTurn{
		ID:               "turn-noimg-1",
		UserID:           "u-1",
		SessionID:        "s-1",
		Model:            "claude-haiku-4-5-20251001",
		RoutedTier:       "simple",
		RouterModel:      "claude-haiku-4-5-20251001",
		CompletionReason: "end_turn",
		StartedAt:        time.Now().UTC(),
		EndedAt:          time.Now().UTC(),
		CreatedAt:        time.Now().UTC(),
	}
	if err := repo.InsertTurn(context.Background(), want); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var gotHadImage int
	if err := repo.db.QueryRow(
		`SELECT had_image FROM agent_turns WHERE id = ?`, want.ID,
	).Scan(&gotHadImage); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotHadImage != 0 {
		t.Fatalf("had_image: got %d want 0", gotHadImage)
	}
}
