package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

func TestQuoteReroll_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteQuoteRerollRepository(db)

	if err := repo.Upsert(ctx, "u1", "2026-08-08", 3); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LocalDate != "2026-08-08" {
		t.Errorf("local date = %q, want %q", got.LocalDate, "2026-08-08")
	}
	if got.Offset != 3 {
		t.Errorf("offset = %d, want 3", got.Offset)
	}
}

func TestQuoteReroll_NotFoundForAFreshUser(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteQuoteRerollRepository(db)

	// A user who has never rerolled has no row, and the caller resolves that
	// to the day's quote. This must be a distinguishable sentinel rather than
	// a zero value, because offset 0 is also a legitimate stored state.
	_, err := repo.Get(ctx, "u1")
	if !errors.Is(err, ErrQuoteRerollNotFound) {
		t.Fatalf("get on a fresh user = %v, want ErrQuoteRerollNotFound", err)
	}
}

func TestQuoteReroll_UpsertOverwritesTheSingleRow(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteQuoteRerollRepository(db)

	// The table's whole premise: rerolling repeatedly, and across days, never
	// accumulates rows. If this ever grows past 1 the table needs a reaper.
	for _, d := range []string{"2026-08-07", "2026-08-08", "2026-08-08"} {
		if err := repo.Upsert(ctx, "u1", d, 1); err != nil {
			t.Fatalf("upsert %s: %v", d, err)
		}
	}

	var rows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM user_dashboard_quote_rerolls WHERE user_id = 'u1'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("stored %d rows after 3 rerolls, want 1", rows)
	}

	got, err := repo.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LocalDate != "2026-08-08" {
		t.Errorf("local date = %q, want the most recent %q", got.LocalDate, "2026-08-08")
	}
}

func TestQuoteReroll_IsPerUser(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	mustInsertUser(t, db, "u2")
	repo := NewSQLiteQuoteRerollRepository(db)

	if err := repo.Upsert(ctx, "u1", "2026-08-08", 5); err != nil {
		t.Fatalf("upsert u1: %v", err)
	}
	if _, err := repo.Get(ctx, "u2"); !errors.Is(err, ErrQuoteRerollNotFound) {
		t.Errorf("u2 sees u1's reroll: %v", err)
	}
}

func TestQuoteReroll_CascadesOnUserDelete(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteQuoteRerollRepository(db)

	if err := repo.Upsert(ctx, "u1", "2026-08-08", 2); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = 'u1'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	var rows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM user_dashboard_quote_rerolls`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d reroll rows survived the user delete, want 0", rows)
	}
}

// A closed database is the cheapest way to produce a real driver error, so the
// error wrapping is exercised rather than assumed.
func TestQuoteReroll_WrapsDriverErrors(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mustInsertUser(t, db, "u1")
	repo := NewSQLiteQuoteRerollRepository(db)
	closeDB(t, db)

	if _, err := repo.Get(ctx, "u1"); err == nil || errors.Is(err, ErrQuoteRerollNotFound) {
		t.Errorf("get on a closed db = %v, want a wrapped driver error", err)
	}
	if err := repo.Upsert(ctx, "u1", "2026-08-08", 1); err == nil {
		t.Error("upsert on a closed db succeeded")
	}
}

func closeDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
}
