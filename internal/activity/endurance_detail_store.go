package activity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// sqliteEnduranceDetailStore is the shared DetailStore implementation for
// the four endurance-shaped types, parameterized by detail table. One Go
// implementation, four tables — the accepted duplication lives in the
// schema, not the code.
type sqliteEnduranceDetailStore struct {
	db    *sql.DB
	table string
}

var _ DetailStore = (*sqliteEnduranceDetailStore)(nil)

// NewSQLiteEnduranceDetailStore builds the detail store for one endurance
// type. Panics when t has no detail table (a wiring bug: base-only types
// must register a nil DetailStore instead).
func NewSQLiteEnduranceDetailStore(db *sql.DB, t ActivityType) DetailStore {
	table := detailTable(t)
	if table == "" {
		panic(fmt.Sprintf("activity: %q has no endurance detail table", t))
	}
	return &sqliteEnduranceDetailStore{db: db, table: table}
}

// Load returns the *EnduranceDetails for a live, owned activity.
// ErrNotFound covers missing row, soft-deleted base, wrong owner, and
// no-detail-row alike — indistinguishable by design.
func (s *sqliteEnduranceDetailStore) Load(ctx context.Context, userID, activityID string) (any, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT d.distance_meters, d.raw_distance_meters, d.avg_pace_sec_per_km,
		       d.best_pace_sec_per_km, d.elevation_gain_meters,
		       d.elevation_loss_meters, d.elevation_high_meters, d.elevation_low_meters,
		       d.environment, d.route_geojson
		FROM `+s.table+` d
		JOIN activities a ON a.id = d.activity_id
		WHERE d.activity_id = ? AND a.user_id = ? AND a.deleted_at IS NULL
	`, activityID, userID)
	var d EnduranceDetails
	if err := row.Scan(&d.DistanceMeters, &d.RawDistanceMeters, &d.AvgPaceSecPerKm,
		&d.BestPaceSecPerKm, &d.ElevationGainMeters,
		&d.ElevationLossMeters, &d.ElevationHighMeters, &d.ElevationLowMeters,
		&d.Environment, &d.RouteGeoJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// Save upserts the detail row for a live, owned activity. The ownership
// check runs in the same transaction as the write so a concurrent
// soft-delete can't slip a detail row under a dead base row.
func (s *sqliteEnduranceDetailStore) Save(ctx context.Context, userID, activityID string, details any) error {
	d, ok := details.(*EnduranceDetails)
	if !ok {
		return fmt.Errorf("activity: endurance detail store got %T, want *EnduranceDetails", details)
	}
	env := d.Environment
	if env == "" {
		// NOT NULL column with a CHECK; default matches the ingest path.
		env = EnvironmentOutdoor
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := ownsLiveActivityTx(ctx, tx, userID, activityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO `+s.table+` (
			activity_id, distance_meters, raw_distance_meters,
			avg_pace_sec_per_km, best_pace_sec_per_km, elevation_gain_meters,
			elevation_loss_meters, elevation_high_meters, elevation_low_meters,
			environment, route_geojson
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(activity_id) DO UPDATE SET
			distance_meters = excluded.distance_meters,
			raw_distance_meters = excluded.raw_distance_meters,
			avg_pace_sec_per_km = excluded.avg_pace_sec_per_km,
			best_pace_sec_per_km = excluded.best_pace_sec_per_km,
			elevation_gain_meters = excluded.elevation_gain_meters,
			elevation_loss_meters = excluded.elevation_loss_meters,
			elevation_high_meters = excluded.elevation_high_meters,
			elevation_low_meters = excluded.elevation_low_meters,
			environment = excluded.environment,
			route_geojson = excluded.route_geojson
	`, activityID, d.DistanceMeters, d.RawDistanceMeters,
		d.AvgPaceSecPerKm, d.BestPaceSecPerKm, d.ElevationGainMeters,
		d.ElevationLossMeters, d.ElevationHighMeters, d.ElevationLowMeters,
		env, d.RouteGeoJSON); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete removes the detail row for a live, owned activity. Deleting an
// activity that has no detail row is a no-op (idempotent); an unowned or
// dead activity is ErrNotFound.
func (s *sqliteEnduranceDetailStore) Delete(ctx context.Context, userID, activityID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := ownsLiveActivityTx(ctx, tx, userID, activityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE activity_id = ?`, activityID); err != nil {
		return err
	}
	return tx.Commit()
}

// ownsLiveActivityTx asserts the base row exists, is live, and belongs to
// userID; anything else reads as ErrNotFound.
func ownsLiveActivityTx(ctx context.Context, tx *sql.Tx, userID, activityID string) error {
	var one int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM activities WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, activityID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
