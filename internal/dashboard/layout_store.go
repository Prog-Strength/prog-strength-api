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

// Get reads the stored sections blob and normalizes it. Normalization on read
// is what lets migration 055's blob stay CHECK-free: a layout written before a
// rule existed, or naming a tile since retired, is repaired here rather than
// erroring the dashboard.
func (r *SQLiteRepository) Get(ctx context.Context, userID string) (Layout, error) {
	var raw string
	err := r.db.QueryRowContext(ctx,
		`SELECT sections FROM user_dashboard_layouts WHERE user_id = ?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Layout{}, ErrLayoutNotFound
	}
	if err != nil {
		return Layout{}, fmt.Errorf("dashboard: get layout: %w", err)
	}
	var sections []Section
	if err := json.Unmarshal([]byte(raw), &sections); err != nil {
		return Layout{}, fmt.Errorf("dashboard: decode layout: %w", err)
	}
	return Normalize(sections), nil
}

func (r *SQLiteRepository) Upsert(ctx context.Context, userID string, sections []Section) error {
	if sections == nil {
		sections = []Section{}
	}
	blob, err := json.Marshal(sections)
	if err != nil {
		return fmt.Errorf("dashboard: encode layout: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO user_dashboard_layouts (user_id, sections, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			sections   = excluded.sections,
			updated_at = excluded.updated_at
	`, userID, string(blob), r.now().UTC())
	if err != nil {
		return fmt.Errorf("dashboard: upsert layout: %w", err)
	}
	return nil
}
