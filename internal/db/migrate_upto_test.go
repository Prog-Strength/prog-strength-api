package db

import (
	"context"
	"database/sql"
)

// migrateUpTo applies all registered migrations with Version <= max. Test-only:
// lets migration tests build a fixture at schema N-1, seed rows, then apply N
// (via Migrate) and assert on the migrated state or the failure mode.
func migrateUpTo(sqldb *sql.DB, maxVersion int) error {
	ctx := context.Background()
	if err := ensureMigrationsTable(ctx, sqldb); err != nil {
		return err
	}
	migrations, err := collectMigrations(registeredGoMigrations())
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.Version > maxVersion {
			break
		}
		applied, err := isApplied(ctx, sqldb, m.Version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := applyMigration(ctx, sqldb, m); err != nil {
			return err
		}
	}
	return nil
}
