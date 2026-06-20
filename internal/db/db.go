package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	// Registers the sqlite3 driver under the name "sqlite3" so sql.Open
	// can resolve it. The blank import is the standard driver-registration
	// pattern from database/sql.
	_ "github.com/mattn/go-sqlite3"
)

// why: sqlite_vec.Auto registers the extension via sqlite3_auto_extension
// against the same statically-linked SQLite the mattn driver uses, so every
// connection opened afterwards (including dbtest and migrations) gets the vec0
// virtual table. Done in init so it runs before any db.Open; the registration
// is global and idempotent. See prog-strength-docs/sows/agent-vector-memory.md.
func init() {
	sqlite_vec.Auto()
}

// Open opens a SQLite database at the given path and configures connection pooling.
// The path is typically a file path like "/data/app.db" or ":memory:" for in-memory.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite benefits from a single writer connection and a small reader pool.
	// WAL mode allows concurrent reads while a write is in progress.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	// Verify connection works. Use PingContext with a short bounded
	// timeout so a slow disk doesn't hang startup indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return db, nil
}
