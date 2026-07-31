package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var _ Repository = (*SQLiteRepository)(nil)

type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteLayoutRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

func (r *SQLiteRepository) Get(ctx context.Context, userID string) (Layout, error) {
	var raw string
	err := r.db.QueryRowContext(ctx,
		`SELECT tile_ids FROM user_dashboard_layouts WHERE user_id = ?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Layout{}, ErrLayoutNotFound
	}
	if err != nil {
		return Layout{}, fmt.Errorf("dashboard: get layout: %w", err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return Layout{}, fmt.Errorf("dashboard: decode layout: %w", err)
	}
	out := make([]TileID, 0, len(ids))
	for _, id := range ids {
		if ValidTileID(TileID(id)) {
			out = append(out, TileID(id))
		}
	}
	return Layout{TileIDs: out}, nil
}

func (r *SQLiteRepository) Upsert(ctx context.Context, userID string, tileIDs []TileID) error {
	if tileIDs == nil {
		tileIDs = []TileID{}
	}
	blob, err := json.Marshal(tileIDs)
	if err != nil {
		return fmt.Errorf("dashboard: encode layout: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO user_dashboard_layouts (user_id, tile_ids, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			tile_ids   = excluded.tile_ids,
			updated_at = excluded.updated_at
	`, userID, string(blob), r.now().UTC())
	if err != nil {
		return fmt.Errorf("dashboard: upsert layout: %w", err)
	}
	return nil
}
