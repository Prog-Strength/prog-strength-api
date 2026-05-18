package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed telemetry_migrations/*.sql
var telemetryMigrationsFS embed.FS

// MigrateTelemetry runs pending telemetry-database migrations against
// the given handle. Operates on the same scheme as Migrate (numeric
// prefix on filename, schema_migrations tracking table, transactional
// per-migration apply) but reads from telemetry_migrations/*.sql.
//
// Kept as a parallel function rather than a refactor of Migrate so
// the existing app-db migration path is untouched.
func MigrateTelemetry(db *sql.DB) error {
	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("ensure telemetry migrations table: %w", err)
	}

	entries, err := telemetryMigrationsFS.ReadDir("telemetry_migrations")
	if err != nil {
		return fmt.Errorf("read telemetry migrations dir: %w", err)
	}

	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(entry.Name())
		if err != nil {
			log.Printf("skip malformed telemetry migration %s: %v", entry.Name(), err)
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

	for _, m := range migrations {
		applied, err := isApplied(db, m.Version)
		if err != nil {
			return fmt.Errorf("check if telemetry migration %d applied: %w", m.Version, err)
		}
		if applied {
			continue
		}

		log.Printf("applying telemetry migration %d: %s", m.Version, m.Filename)
		if err := applyTelemetryMigration(db, m); err != nil {
			return fmt.Errorf("apply telemetry migration %d: %w", m.Version, err)
		}
	}

	log.Println("telemetry migrations complete")
	return nil
}

// applyTelemetryMigration is the telemetry-fs sibling of applyMigration.
// Tiny code duplication — the two could be unified by parameterizing
// the embed.FS, but the existing app-db function stays untouched.
func applyTelemetryMigration(db *sql.DB, m migration) error {
	content, err := telemetryMigrationsFS.ReadFile(filepath.Join("telemetry_migrations", m.Filename))
	if err != nil {
		return fmt.Errorf("read migration file: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(string(content)); err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}

	if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.Version); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit()
}
