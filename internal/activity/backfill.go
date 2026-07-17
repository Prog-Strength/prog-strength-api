package activity

import (
	"context"
	"fmt"
	"log"
)

// BackfillActivityBestEfforts populates activity_best_efforts from the
// archived TCX of every live running activity, for the one-time migration
// from "no best efforts" to "best efforts on every run". Idempotent: gated
// on the table being empty, so reruns (and every subsequent boot) are
// no-ops.
//
// Re-parsing from S3 rather than the in-DB downsampled trackpoints is
// deliberate — the ~300-point downsample is too coarse for honest
// 1-mile-window math; the S3 object is the canonical raw stream and is
// exactly what summarize sees on a fresh upload.
//
// Per SOW Open Question #4: a missing-from-S3 or unparseable TCX is logged
// (with the activity ID) and skipped, not a hard boot failure — operator-
// induced state divergence shouldn't take the API down. The boot logs a
// summary count so the skip volume is visible.
func (r *SQLiteRepository) BackfillActivityBestEfforts(ctx context.Context) error {
	var existing int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_best_efforts`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		// Already populated — nothing to do.
		return nil
	}

	// Pull the live running activities that need backfilling. The slice is
	// materialized up front so the per-activity transactions below don't
	// run inside an open read cursor.
	type target struct {
		id     string
		s3Key  string
		userID string
	}
	var targets []target
	if err := func() error {
		rows, err := r.db.QueryContext(ctx, `
			SELECT id, tcx_s3_key, user_id
			FROM activities
			WHERE activity_type = ? AND deleted_at IS NULL
		`, ActivityRunning)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.id, &t.s3Key, &t.userID); err != nil {
				return err
			}
			targets = append(targets, t)
		}
		return rows.Err()
	}(); err != nil {
		return err
	}

	if len(targets) == 0 {
		return nil
	}

	var processed, skipped int
	for _, t := range targets {
		body, err := r.archiver.Get(ctx, t.s3Key)
		if err != nil {
			log.Printf("backfill best efforts: skip activity_id=%s user_id=%s: fetch tcx: %v", t.id, t.userID, err)
			skipped++
			continue
		}
		parsed, err := parseTCX(body)
		if err != nil {
			log.Printf("backfill best efforts: skip activity_id=%s user_id=%s: parse tcx: %v", t.id, t.userID, err)
			skipped++
			continue
		}
		if err := validate(parsed); err != nil {
			log.Printf("backfill best efforts: skip activity_id=%s user_id=%s: validate tcx: %v", t.id, t.userID, err)
			skipped++
			continue
		}

		efforts := bestEfforts(parsed.Trackpoints, StandardDistances)
		if err := r.insertBackfilledEfforts(ctx, t.id, efforts); err != nil {
			log.Printf("backfill best efforts: skip activity_id=%s user_id=%s: insert rows: %v", t.id, t.userID, err)
			skipped++
			continue
		}
		processed++
	}

	log.Printf("backfill best efforts: complete processed=%d skipped=%d total=%d", processed, skipped, len(targets))
	return nil
}

// BackfillBestEffortWindowBounds populates window_start/end columns for rows
// that predate the v2 migration. Idempotent: only touches rows with NULL
// window bounds. Re-parses archived TCX at full resolution.
func (r *SQLiteRepository) BackfillBestEffortWindowBounds(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT e.activity_id, a.tcx_s3_key, a.user_id
		FROM activity_best_efforts e
		JOIN activities a ON a.id = e.activity_id
		WHERE e.window_start_elapsed_seconds IS NULL
		GROUP BY e.activity_id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type target struct {
		id, s3Key, userID string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.s3Key, &t.userID); err != nil {
			return err
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}

	var updated, skipped int
	for _, t := range targets {
		body, err := r.archiver.Get(ctx, t.s3Key)
		if err != nil {
			log.Printf("backfill best effort windows: skip activity_id=%s: fetch tcx: %v", t.id, err)
			skipped++
			continue
		}
		parsed, err := parseTCX(body)
		if err != nil {
			log.Printf("backfill best effort windows: skip activity_id=%s: parse: %v", t.id, err)
			skipped++
			continue
		}
		if valErr := validate(parsed); valErr != nil {
			skipped++
			continue
		}
		efforts := bestEfforts(parsed.Trackpoints, StandardDistances)
		byKey := make(map[string]ActivityBestEffort, len(efforts))
		for _, e := range efforts {
			byKey[e.DistanceKey] = e
		}

		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		rowRows, err := tx.QueryContext(ctx, `
			SELECT distance_key FROM activity_best_efforts WHERE activity_id = ?
		`, t.id)
		if err != nil {
			tx.Rollback()
			return err
		}
		defer rowRows.Close()
		var keys []string
		for rowRows.Next() {
			var k string
			if scanErr := rowRows.Scan(&k); scanErr != nil {
				tx.Rollback()
				return scanErr
			}
			keys = append(keys, k)
		}
		if err := rowRows.Err(); err != nil {
			tx.Rollback()
			return err
		}

		for _, k := range keys {
			e, ok := byKey[k]
			if !ok {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE activity_best_efforts
				SET window_start_elapsed_seconds = ?, window_end_elapsed_seconds = ?
				WHERE activity_id = ? AND distance_key = ?
			`, e.WindowStartElapsedSeconds, e.WindowEndElapsedSeconds, t.id, k); err != nil {
				tx.Rollback()
				return fmt.Errorf("update windows activity_id=%s: %w", t.id, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		updated++
	}
	log.Printf("backfill best effort windows: complete updated=%d skipped=%d total=%d", updated, skipped, len(targets))
	return nil
}

// BackfillActivityRoutes re-parses the archived TCX of every live outdoor
// activity still missing route_geojson and writes the same simplified route
// (plus the matching downsampled trackpoint lat/lon) the live ingest path
// would have produced. Idempotent: a row leaves the selection set the moment
// its route is written, so reruns and later boots only touch rows that are
// still unmapped.
//
// Selection is outdoor-only on purpose. Indoor runs keep route_geojson NULL
// forever, so selecting all NULL routes would re-fetch every treadmill TCX on
// every boot; outdoor + NULL is the historical gap the trail-map SOW left.
//
// TEMPORARY — this exists only to fill route geometry for pre-trail-map
// outdoor activities by re-parsing their archived TCX. It is safe to delete
// once prod outdoor history has maps: live ingest already writes
// route_geojson, so this only repairs the past. See
// sows/sow-trail-map-backfill.md.
func (r *SQLiteRepository) BackfillActivityRoutes(ctx context.Context) error {
	// Materialize the targets up front (inside a closed read cursor) so the
	// per-activity write transactions below don't run against an open query.
	type target struct {
		id      string
		s3Key   string
		userID  string
		actType ActivityType
	}
	var targets []target
	if err := func() error {
		// strength_training rows also default to environment='outdoor' and
		// carry a NULL route (they're stationary — summarizeStrength never
		// builds one). Excluding them keeps this from re-fetching every
		// strength TCX from S3 on every boot — the same waste the outdoor-only
		// filter avoids for treadmill runs — and stops a misattached
		// GPS-bearing strength TCX from being handed the run route pipeline.
		// Running/walking/cycling/other all flow through summarize + buildRoute,
		// so they stay in scope.
		rows, err := r.db.QueryContext(ctx, `
			SELECT id, tcx_s3_key, user_id, activity_type
			FROM activities
			WHERE deleted_at IS NULL
			  AND environment = ?
			  AND activity_type != ?
			  AND route_geojson IS NULL
			  AND tcx_s3_key IS NOT NULL
			  AND tcx_s3_key != ''
		`, EnvironmentOutdoor, ActivityStrengthTraining)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.id, &t.s3Key, &t.userID, &t.actType); err != nil {
				return err
			}
			targets = append(targets, t)
		}
		return rows.Err()
	}(); err != nil {
		return err
	}

	if len(targets) == 0 {
		return nil
	}

	var processed, skipped int
	for _, t := range targets {
		body, err := r.archiver.Get(ctx, t.s3Key)
		if err != nil {
			log.Printf("backfill activity routes: skip activity_id=%s user_id=%s: fetch tcx: %v", t.id, t.userID, err)
			skipped++
			continue
		}
		parsed, err := parseTCX(body)
		if err != nil {
			log.Printf("backfill activity routes: skip activity_id=%s user_id=%s: parse tcx: %v", t.id, t.userID, err)
			skipped++
			continue
		}
		if err := validate(parsed); err != nil {
			log.Printf("backfill activity routes: skip activity_id=%s user_id=%s: validate tcx: %v", t.id, t.userID, err)
			skipped++
			continue
		}

		// Shared geometry primitives — do not fork the RDP/gap algorithm.
		route := buildRoute(parsed.Trackpoints)
		if route == nil {
			// Parsed fine but carried no usable Position geometry (e.g. an
			// outdoor-tagged file with no GPS). Correct outcome is a NULL
			// route; make the skip visible in the summary counts.
			log.Printf("backfill activity routes: nothing to map activity_id=%s", t.id)
			skipped++
			continue
		}
		// validate guarantees len(parsed.Trackpoints) >= 1, so [0] is safe.
		pts := downsample(parsed.Trackpoints, parsed.Trackpoints[0], t.actType)

		// One transaction per activity: a mid-loop crash leaves finished rows
		// done and the rest still selected next boot (natural resume). A real DB
		// write error (unlike a skippable fetch/parse failure) ends this pass and
		// is returned to the caller, which soft-logs it; the boot continues and
		// the unprocessed rows are retried next time.
		if err := r.writeBackfilledRoute(ctx, t.id, *route, pts); err != nil {
			return fmt.Errorf("update route activity_id=%s: %w", t.id, err)
		}
		processed++
	}

	log.Printf("backfill activity routes: complete processed=%d skipped=%d total=%d", processed, skipped, len(targets))
	return nil
}

// writeBackfilledRoute persists one activity's route and downsampled
// trackpoint coordinates in a single transaction. The stored trackpoint rows
// came from this same downsample on the same bytes, so sequences line up —
// coordinates are updated in place by sequence, never inserted or deleted.
func (r *SQLiteRepository) writeBackfilledRoute(ctx context.Context, activityID, route string, pts []Trackpoint) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.ExecContext(ctx, `
		UPDATE activities SET route_geojson = ? WHERE id = ?
	`, route, activityID); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, `
		UPDATE activity_trackpoints
		SET latitude = ?, longitude = ?
		WHERE activity_id = ? AND sequence = ?
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range pts {
		if _, err := stmt.ExecContext(ctx, p.Latitude, p.Longitude, activityID, p.Sequence); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertBackfilledEfforts writes one activity's best-effort rows in its own
// transaction. A target with zero efforts (too short for any distance)
// still counts as processed — there's simply nothing to insert.
func (r *SQLiteRepository) insertBackfilledEfforts(ctx context.Context, activityID string, efforts []ActivityBestEffort) error {
	if len(efforts) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertBestEffortsTx(ctx, tx, activityID, efforts); err != nil {
		return err
	}
	return tx.Commit()
}
