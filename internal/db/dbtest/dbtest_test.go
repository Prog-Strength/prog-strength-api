package dbtest

import (
	"database/sql"
	"errors"
	"testing"
)

// TestNew_OpensMigratedDB verifies New returns a live, migrated database:
// it must ping and have recorded at least one migration in the
// schema_migrations ledger (the table Migrate tracks applied versions in).
func TestNew_OpensMigratedDB(t *testing.T) {
	database := New(t)

	if err := database.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	var count int
	if err := database.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count <= 0 {
		t.Fatalf("expected at least one applied migration, got %d", count)
	}
}

// TestNew_IsIsolatedPerCall verifies each call yields a genuinely separate
// database: a table created on the first handle must not be visible on the
// second, so tests can't leak state into one another. We probe via a custom
// table and a sqlite_master lookup to avoid coupling to the domain schema.
func TestNew_IsIsolatedPerCall(t *testing.T) {
	first := New(t)
	second := New(t)

	if _, err := first.Exec("CREATE TABLE dbtest_probe (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create probe table on first db: %v", err)
	}

	var name string
	err := second.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'dbtest_probe'",
	).Scan(&name)
	if err == nil {
		t.Fatal("second db saw dbtest_probe created on first db: databases are not isolated")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("lookup probe table on second db: %v", err)
	}
}
