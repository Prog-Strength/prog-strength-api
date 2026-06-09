package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// newTestLedgerDB opens a fresh telemetry.db in t.TempDir() with all
// telemetry migrations applied (so agent_turns and agent_speak_calls
// exist), mirroring the helper in internal/telemetry's repo tests.
func newTestLedgerDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open telemetry db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.MigrateTelemetry(conn); err != nil {
		t.Fatalf("migrate telemetry: %v", err)
	}
	return conn
}

func insertTurn(t *testing.T, conn *sql.DB, id, userID, model string, in, out, cacheCreate, cacheRead int64, startedAt time.Time) {
	t.Helper()
	_, err := conn.Exec(`
		INSERT INTO agent_turns (
			id, user_id, session_id, model, routed_tier, router_model,
			router_latency_ms, input_tokens, output_tokens,
			cache_creation_tokens, cache_read_tokens,
			total_latency_ms, time_to_first_token_ms, completion_reason,
			started_at, ended_at, created_at
		) VALUES (?, ?, 's', ?, 'simple', ?, 0, ?, ?, ?, ?, 0, 0, 'end_turn', ?, ?, ?)
	`, id, userID, model, model, in, out, cacheCreate, cacheRead, startedAt, startedAt, startedAt)
	if err != nil {
		t.Fatalf("insert turn: %v", err)
	}
}

func insertSpeak(t *testing.T, conn *sql.DB, id, userID, model string, chars int64, startedAt time.Time) {
	t.Helper()
	_, err := conn.Exec(`
		INSERT INTO agent_speak_calls (
			id, user_id, session_id, model, chars, voice, started_at, ended_at, error
		) VALUES (?, ?, NULL, ?, ?, 'alloy', ?, ?, NULL)
	`, id, userID, model, chars, startedAt, startedAt)
	if err != nil {
		t.Fatalf("insert speak: %v", err)
	}
}

func newTestLedger(t *testing.T) (*Ledger, *sql.DB) {
	conn := newTestLedgerDB(t)
	pt, err := LoadPriceTable(sowExampleJSON)
	if err != nil {
		t.Fatalf("price table: %v", err)
	}
	return NewLedger(conn, pt), conn
}

func TestSpendTodayUSD_ZeroRows(t *testing.T) {
	l, _ := newTestLedger(t)
	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	got, err := l.SpendTodayUSD(context.Background(), "u-1", start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if got != 0 {
		t.Fatalf("zero rows: got %v want 0", got)
	}
}

func TestSpendTodayUSD_SingleTurn(t *testing.T) {
	l, conn := newTestLedger(t)
	at := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	// 1M input on sonnet = $3.00.
	insertTurn(t, conn, "t1", "u-1", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, at)

	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	got, err := l.SpendTodayUSD(context.Background(), "u-1", start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !approx(got, 3.00) {
		t.Fatalf("single turn: got %v want 3.00", got)
	}
}

func TestSpendTodayUSD_MultiModel(t *testing.T) {
	l, conn := newTestLedger(t)
	at := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	insertTurn(t, conn, "t1", "u-1", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, at)         // $3.00
	insertTurn(t, conn, "t2", "u-1", "claude-haiku-4-5-20251001", 0, 1_000_000, 0, 0, at) // $4.00

	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	got, err := l.SpendTodayUSD(context.Background(), "u-1", start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !approx(got, 7.00) {
		t.Fatalf("multi model: got %v want 7.00", got)
	}
}

func TestSpendTodayUSD_ExcludesOutsideWindow(t *testing.T) {
	l, conn := newTestLedger(t)
	inside := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	before := time.Date(2026, 6, 8, 23, 0, 0, 0, time.UTC)
	after := time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC)
	insertTurn(t, conn, "t-in", "u-1", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, inside)
	insertTurn(t, conn, "t-before", "u-1", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, before)
	insertTurn(t, conn, "t-after", "u-1", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, after)

	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	got, err := l.SpendTodayUSD(context.Background(), "u-1", start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !approx(got, 3.00) {
		t.Fatalf("window filter: got %v want 3.00 (only the inside row)", got)
	}
}

func TestSpendTodayUSD_SpeakContributes(t *testing.T) {
	l, conn := newTestLedger(t)
	at := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	// 200k chars at $15/Mchar = $3.00.
	insertSpeak(t, conn, "sp1", "u-1", "tts-1", 200_000, at)

	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	got, err := l.SpendTodayUSD(context.Background(), "u-1", start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !approx(got, 3.00) {
		t.Fatalf("speak only: got %v want 3.00", got)
	}
}

func TestSpendTodayUSD_CombinedTurnsAndSpeak(t *testing.T) {
	l, conn := newTestLedger(t)
	at := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	insertTurn(t, conn, "t1", "u-1", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, at) // $3.00
	insertSpeak(t, conn, "sp1", "u-1", "tts-1", 200_000, at)                      // $3.00
	// Another user's spend must not leak in.
	insertTurn(t, conn, "t2", "u-2", "claude-sonnet-4-6", 1_000_000, 0, 0, 0, at)

	start := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	got, err := l.SpendTodayUSD(context.Background(), "u-1", start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !approx(got, 6.00) {
		t.Fatalf("combined: got %v want 6.00", got)
	}
}
