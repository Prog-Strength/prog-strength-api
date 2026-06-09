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

func TestInsertSpeakCall_PersistsAndReadsBack(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()

	sess := "s-1"
	errMsg := "rate_limited"
	want := AgentSpeakCall{
		ID:        "sp-1",
		UserID:    "u-1",
		SessionID: &sess,
		Model:     "gpt-4o-mini-tts",
		Chars:     184,
		Voice:     "alloy",
		StartedAt: time.Now().UTC().Truncate(time.Second),
		EndedAt:   time.Now().UTC().Truncate(time.Second),
		Error:     &errMsg,
	}
	if err := repo.InsertSpeakCall(context.Background(), want); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var (
		gotUser  string
		gotSess  sql.NullString
		gotModel string
		gotChars int64
		gotVoice string
		gotErr   sql.NullString
	)
	err := repo.db.QueryRow(
		`SELECT user_id, session_id, model, chars, voice, error
		   FROM agent_speak_calls WHERE id = ?`, want.ID,
	).Scan(&gotUser, &gotSess, &gotModel, &gotChars, &gotVoice, &gotErr)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotUser != "u-1" || !gotSess.Valid || gotSess.String != "s-1" ||
		gotModel != "gpt-4o-mini-tts" || gotChars != 184 || gotVoice != "alloy" ||
		!gotErr.Valid || gotErr.String != "rate_limited" {
		t.Fatalf("speak row mismatch: user=%q sess=%v model=%q chars=%d voice=%q err=%v",
			gotUser, gotSess, gotModel, gotChars, gotVoice, gotErr)
	}
}

func TestInsertSpeakCall_NullSessionAndError(t *testing.T) {
	repo, cleanup := newTestTelemetryRepo(t)
	defer cleanup()

	want := AgentSpeakCall{
		ID:        "sp-2",
		UserID:    "u-1",
		SessionID: nil,
		Model:     "tts-1",
		Chars:     42,
		Voice:     "verse",
		StartedAt: time.Now().UTC(),
		EndedAt:   time.Now().UTC(),
		Error:     nil,
	}
	if err := repo.InsertSpeakCall(context.Background(), want); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var gotSess, gotErr sql.NullString
	if err := repo.db.QueryRow(
		`SELECT session_id, error FROM agent_speak_calls WHERE id = ?`, want.ID,
	).Scan(&gotSess, &gotErr); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if gotSess.Valid || gotErr.Valid {
		t.Fatalf("expected NULL session_id and error, got sess=%v err=%v", gotSess, gotErr)
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
