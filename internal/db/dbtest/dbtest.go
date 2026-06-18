// Package dbtest provides an ephemeral SQLite database for tests: a
// throwaway temp-file DB with the embedded migrations applied. A temp
// file (not ":memory:") matches production pooling semantics — the app
// uses a multi-connection pool, and a ":memory:" DSN would give each
// pooled connection its own empty database.
package dbtest

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
)

// New opens a migrated, throwaway SQLite database under t.TempDir() and
// registers cleanup to close it.
func New(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("dbtest: open: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		_ = database.Close()
		t.Fatalf("dbtest: migrate: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
