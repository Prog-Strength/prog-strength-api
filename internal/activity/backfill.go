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
