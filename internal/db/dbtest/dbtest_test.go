package dbtest

import "testing"

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

// TestNew_IsIsolatedPerCall verifies each call yields a separate database
// handle so tests don't share state through a single pooled connection.
func TestNew_IsIsolatedPerCall(t *testing.T) {
	first := New(t)
	second := New(t)

	if first == second {
		t.Fatal("expected distinct *sql.DB handles per call, got the same pointer")
	}
}
