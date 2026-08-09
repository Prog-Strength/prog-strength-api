package weather

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/id"
)

// Compile-time checks that the SQLite implementations satisfy the seams.
var (
	_ CacheRepository     = (*SQLiteCacheRepository)(nil)
	_ LocationsRepository = (*SQLiteLocationsRepository)(nil)
)

// cacheEvictionAge matches nutritionlookup's evictionAge and is deliberately
// longer than the 30-day geocoding TTL so a geocode row is never evicted at
// the exact moment it goes stale.
const cacheEvictionAge = 90 * 24 * time.Hour

type SQLiteCacheRepository struct {
	db *sql.DB
	// now is injectable so tests can time-travel the eviction policy —
	// same pattern as nutritionlookup.
	now func() time.Time
}

func NewSQLiteCacheRepository(db *sql.DB) *SQLiteCacheRepository {
	return &SQLiteCacheRepository{db: db, now: time.Now}
}

func (r *SQLiteCacheRepository) Get(ctx context.Context, key string) (*CacheRow, error) {
	var row CacheRow
	err := r.db.QueryRowContext(ctx, `
		SELECT cache_key, payload_json, fetched_at, last_used_at
		FROM weather_cache
		WHERE cache_key = ?
	`, key).Scan(&row.CacheKey, &row.PayloadJSON, &row.FetchedAt, &row.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Bump last_used_at on every hit — the eviction signal. Places the user
	// actually checks stay hot; one-off lookups age out.
	now := r.now().UTC()
	if _, err := r.db.ExecContext(ctx, `
		UPDATE weather_cache SET last_used_at = ? WHERE cache_key = ?
	`, now, key); err != nil {
		return nil, err
	}
	row.LastUsedAt = now
	return &row, nil
}

func (r *SQLiteCacheRepository) Put(ctx context.Context, row CacheRow) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO weather_cache (
			cache_key, payload_json, fetched_at, last_used_at
		) VALUES (?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			payload_json = excluded.payload_json,
			fetched_at   = excluded.fetched_at,
			last_used_at = excluded.last_used_at
	`, row.CacheKey, row.PayloadJSON, row.FetchedAt.UTC(), row.LastUsedAt.UTC()); err != nil {
		return err
	}

	// Opportunistic eviction sweep, piggybacked on the write path so the
	// table stays bounded without a background job.
	cutoff := r.now().UTC().Add(-cacheEvictionAge)
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM weather_cache WHERE last_used_at < ?
	`, cutoff)
	return err
}

func (r *SQLiteCacheRepository) LastSuccess(ctx context.Context) (time.Time, error) {
	// MAX() strips the column's DATETIME decltype, so the driver hands the
	// aggregate back as a string rather than a time.Time; scan NullString
	// and parse with the driver's own storage layout. Lexicographic MAX is
	// chronological here because every write stores UTC in one format.
	var raw sql.NullString
	if err := r.db.QueryRowContext(ctx,
		`SELECT MAX(fetched_at) FROM weather_cache`,
	).Scan(&raw); err != nil {
		return time.Time{}, err
	}
	if !raw.Valid {
		return time.Time{}, nil // empty cache: no success yet
	}
	// go-sqlite3 stores time.Time values with this layout.
	return time.Parse("2006-01-02 15:04:05.999999999-07:00", raw.String)
}

type SQLiteLocationsRepository struct {
	db *sql.DB
}

func NewSQLiteLocationsRepository(db *sql.DB) *SQLiteLocationsRepository {
	return &SQLiteLocationsRepository{db: db}
}

func (r *SQLiteLocationsRepository) List(ctx context.Context, userID string) ([]Location, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, user_id, position, label, country, state, lat, lon, created_at
		FROM user_weather_locations
		WHERE user_id = ?
		ORDER BY position
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Location, 0)
	for rows.Next() {
		var loc Location
		var state sql.NullString
		if err := rows.Scan(
			&loc.ID, &loc.UserID, &loc.Position, &loc.Label, &loc.Country,
			&state, &loc.Lat, &loc.Lon, &loc.CreatedAt,
		); err != nil {
			return nil, err
		}
		if state.Valid {
			loc.State = &state.String
		}
		out = append(out, loc)
	}
	return out, rows.Err()
}

// ReplaceAll rewrites the user's whole list in one transaction: delete, then
// insert with position = slice index. Whole-list replace is why the schema
// carries no UNIQUE(user_id, position) — intermediate states never exist for
// readers, and reorders can't collide. Existing ids are preserved so the web
// client's references stay stable; new rows get fresh ids.
func (r *SQLiteLocationsRepository) ReplaceAll(ctx context.Context, userID string, locations []Location) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user_weather_locations WHERE user_id = ?`, userID,
	); err != nil {
		return err
	}
	for i, loc := range locations {
		locID := loc.ID
		if locID == "" {
			locID = id.New()
		}
		createdAt := loc.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		var state any
		if loc.State != nil {
			state = *loc.State
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_weather_locations (
				id, user_id, position, label, country, state, lat, lon, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, locID, userID, i, loc.Label, loc.Country, state, loc.Lat, loc.Lon, createdAt.UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLiteLocationsRepository) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_weather_locations`,
	).Scan(&n)
	return n, err
}
