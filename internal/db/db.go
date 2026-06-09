package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// Registers the sqlite3 driver under the name "sqlite3" so sql.Open
	// can resolve it. The blank import is the standard driver-registration
	// pattern from database/sql.
	_ "github.com/mattn/go-sqlite3"
)

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
