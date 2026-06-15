package db

import (
	"context"
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
//
// Uses context.Background() internally because Migrate runs once during
// startup; there's no request-scoped context to thread through. The
// *Context SQL methods are used regardless so noctx is happy and a
// future Migrate(ctx) variant is a one-line change.
func Migrate(db *sql.DB) error {
	return migrateWith(db, registeredGoMigrations())
}

func migrateWith(db *sql.DB, goMigs []goMigration) error {
	ctx := context.Background()
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}
	migrations, err := collectMigrations(goMigs)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		applied, err := isApplied(ctx, db, m.Version)
		if err != nil {
			return fmt.Errorf("check if migration %d applied: %w", m.Version, err)
		}
		if applied {
			continue
		}
		log.Printf("applying migration %d: %s", m.Version, m.label())
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.Version, err)
		}
	}
	log.Println("migrations complete")
	return nil
}

// goMigration is a registered Go migration. It shares the SQL migrations'
// version ledger and per-migration transaction. A Go migration operates on raw
// SQL via its *sql.Tx and MUST be self-contained, frozen, and DB-only (no
// service calls, no network I/O) so a rebuilt DB always reconciles identically.
type goMigration struct {
	Version int
	Name    string
	Run     func(ctx context.Context, tx *sql.Tx) error
}

// migration is one applyable unit: exactly one of Filename or Run is set.
type migration struct {
	Version  int
	Filename string                                      // SQL migration (empty for Go)
	Name     string                                      // Go migration name (empty for SQL)
	Run      func(ctx context.Context, tx *sql.Tx) error // Go migration body (nil for SQL)
}

func (m migration) label() string {
	if m.Filename != "" {
		return m.Filename
	}
	return "go:" + m.Name
}

// collectMigrations discovers the embedded SQL migrations, appends the
// registered Go migrations, rejects duplicate versions across both sources,
// and returns the combined list sorted by version.
func collectMigrations(goMigs []goMigration) ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	seen := map[int]string{}
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
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%s and %s)", version, prev, entry.Name())
		}
		seen[version] = entry.Name()
		migrations = append(migrations, migration{Version: version, Filename: entry.Name()})
	}
	for _, gm := range goMigs {
		if prev, dup := seen[gm.Version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%s and go:%s)", gm.Version, prev, gm.Name)
		}
		seen[gm.Version] = "go:" + gm.Name
		migrations = append(migrations, migration{Version: gm.Version, Name: gm.Name, Run: gm.Run})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// ensureMigrationsTable creates the schema_migrations table if it doesn't exist.
func ensureMigrationsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

// isApplied checks if a migration version has already been applied.
func isApplied(ctx context.Context, db *sql.DB, version int) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count)
	return count > 0, err
}

// applyMigration runs a migration (SQL file or Go) and records it in
// schema_migrations within the same transaction.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if m.Run != nil {
		if err := m.Run(ctx, tx); err != nil {
			return fmt.Errorf("exec go migration: %w", err)
		}
	} else {
		content, err := migrationsFS.ReadFile(filepath.Join("migrations", m.Filename))
		if err != nil {
			return fmt.Errorf("read migration file: %w", err)
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", m.Version); err != nil {
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
