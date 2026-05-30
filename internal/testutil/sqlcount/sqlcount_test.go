package sqlcount

import (
	"context"
	"testing"
)

// Smoke test: a single CREATE, two INSERTs, and a SELECT should produce
// exactly four statements. Confirms the counter is wired up to both
// ExecContext (CREATE, INSERT) and QueryContext (SELECT).
func TestOpen_CountsExecAndQuery(t *testing.T) {
	db, counter, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t (name) VALUES (?)`, "alice"); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t (name) VALUES (?)`, "bob"); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM t ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	rows.Close()

	if got, want := counter.N(), int64(4); got != want {
		t.Fatalf("counter.N() = %d, want %d", got, want)
	}
}

// Reset must zero the counter so tests can isolate the "code under
// test" from setup statements.
func TestCounter_Reset(t *testing.T) {
	db, counter, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), `CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if counter.N() != 1 {
		t.Fatalf("pre-reset count = %d, want 1", counter.N())
	}
	counter.Reset()
	if counter.N() != 0 {
		t.Fatalf("post-reset count = %d, want 0", counter.N())
	}
}
