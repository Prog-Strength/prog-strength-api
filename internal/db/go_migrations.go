package db

// registeredGoMigrations returns all Go migrations compiled into the binary, in
// no particular order (the runner sorts by version and shares the SQL ledger).
// Each future data backfill appends its migration here so it versions and ships
// in git alongside the schema change that necessitated it.
func registeredGoMigrations() []goMigration {
	return []goMigration{
		migration028(),
		migration029(),
	}
}
