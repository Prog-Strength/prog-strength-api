package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate runs all pending migrations against the given database.
// Migrations are applied in numeric order based on filename prefix (001_, 002_, etc.).
// Already-applied migrations are tracked in the schema_migrations table.
func Migrate(db *sql.DB) error {
	// Ensure schema_migrations table exists.
	// This is safe to run multiple times (CREATE TABLE IF NOT EXISTS).
	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	// Find all migration files.
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Parse and sort migrations by version.
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(entry.Name())
		if err != nil {
			log.Printf("skip malformed migration %s: %v", entry.Name(), err)
			continue
		}
		migrations = append(migrations, migration{
			Version:  version,
			Filename: entry.Name(),
		})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	// Apply pending migrations.
	for _, m := range migrations {
		applied, err := isApplied(db, m.Version)
		if err != nil {
			return fmt.Errorf("check if migration %d applied: %w", m.Version, err)
		}
		if applied {
			continue
		}

		log.Printf("applying migration %d: %s", m.Version, m.Filename)
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.Version, err)
		}
	}

	log.Println("migrations complete")
	return nil
}

type migration struct {
	Version  int
	Filename string
}

// ensureMigrationsTable creates the schema_migrations table if it doesn't exist.
func ensureMigrationsTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

// isApplied checks if a migration version has already been applied.
func isApplied(db *sql.DB, version int) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count)
	return count > 0, err
}

// applyMigration runs a migration file and records it in schema_migrations.
func applyMigration(db *sql.DB, m migration) error {
	// Read migration SQL.
	content, err := migrationsFS.ReadFile(filepath.Join("migrations", m.Filename))
	if err != nil {
		return fmt.Errorf("read migration file: %w", err)
	}

	// Run in a transaction.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Execute migration SQL.
	if _, err := tx.Exec(string(content)); err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}

	// Record migration as applied.
	if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.Version); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit()
}

// parseVersion extracts the numeric prefix from a migration filename.
// Example: "001_initial_schema.sql" -> 1
func parseVersion(filename string) (int, error) {
	base := filepath.Base(filename)
	parts := strings.SplitN(base, "_", 2)
	if len(parts) < 2 {
		return 0, fmt.Errorf("expected format NNN_description.sql, got %s", filename)
	}
	return strconv.Atoi(parts[0])
}
