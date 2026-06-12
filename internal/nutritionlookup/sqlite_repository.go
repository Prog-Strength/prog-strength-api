package nutritionlookup

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

type SQLiteRepository struct {
	db *sql.DB
	// now is injectable so tests can time-travel the freshness and
	// eviction policies — same pattern as the nutrition package.
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

func (r *SQLiteRepository) Get(ctx context.Context, queryNormalized string) (*CacheRow, error) {
	var row CacheRow
	err := r.db.QueryRowContext(ctx, `
		SELECT query_normalized, candidates_json, fetched_at, last_used_at
		FROM nutrition_lookup_cache
		WHERE query_normalized = ?
	`, queryNormalized).Scan(
		&row.QueryNormalized,
		&row.CandidatesJSON,
		&row.FetchedAt,
		&row.LastUsedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Bump last_used_at on every hit — the eviction signal. Foods the
	// users actually eat stay hot forever; one-off lookups age out.
	now := r.now().UTC()
	if _, err := r.db.ExecContext(ctx, `
		UPDATE nutrition_lookup_cache SET last_used_at = ? WHERE query_normalized = ?
	`, now, queryNormalized); err != nil {
		return nil, err
	}
	row.LastUsedAt = now
	return &row, nil
}

func (r *SQLiteRepository) Put(ctx context.Context, row CacheRow) error {
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO nutrition_lookup_cache (
			query_normalized, candidates_json, fetched_at, last_used_at
		) VALUES (?, ?, ?, ?)
		ON CONFLICT(query_normalized) DO UPDATE SET
			candidates_json = excluded.candidates_json,
			fetched_at      = excluded.fetched_at,
			last_used_at    = excluded.last_used_at
	`, row.QueryNormalized, row.CandidatesJSON, row.FetchedAt.UTC(), row.LastUsedAt.UTC()); err != nil {
		return err
	}

	// Opportunistic eviction sweep, piggybacked on the write path so
	// the table stays bounded without a background job.
	cutoff := r.now().UTC().Add(-evictionAge)
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM nutrition_lookup_cache WHERE last_used_at < ?
	`, cutoff)
	return err
}
