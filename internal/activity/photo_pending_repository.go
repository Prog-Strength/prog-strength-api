package activity

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The two-phase upload's repository surface. These sit beside the synchronous
// path's methods rather than replacing them: the old endpoint stays mounted
// through the transition, so both write shapes have to coexist.
//
// See prog-strength-docs/sows/photo-upload-direct-to-s3.md.

// SetUploadKey records the staging object key on a reserved row. Split from
// the insert for the same reason SetS3Key is on the video path: the key embeds
// the photo id, which does not exist until the row does.
func (r *SQLitePhotoRepository) SetUploadKey(ctx context.Context, photoID, key string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET upload_s3_key = ?, updated_at = ?
		WHERE id = ? AND status = ? AND deleted_at IS NULL
	`, key, r.now().UTC(), photoID, PhotoStatusPending)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// MarkProcessing flips a reserved row to 'processing' once commit has
// confirmed the object landed. Scoped by owner and parent so a commit cannot
// advance someone else's reservation, and guarded on the current status so a
// replayed commit is a no-op rather than a second hand-off to the worker.
func (r *SQLitePhotoRepository) MarkProcessing(ctx context.Context, userID, activityID, photoID string, byteSize int64) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET status = ?, byte_size = ?, updated_at = ?
		WHERE id = ? AND activity_id = ? AND user_id = ?
		  AND status = ? AND deleted_at IS NULL
	`, PhotoStatusProcessing, byteSize, r.now().UTC(),
		photoID, activityID, userID, PhotoStatusPending)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// ClaimNextForProcessing takes the oldest row the worker may still attempt and
// increments its attempt counter, returning ok=false when there is nothing to
// do.
//
// The increment happens at CLAIM time, not on failure. That is deliberate: a
// worker that crashes mid-photo — OOM, instance termination — never gets to
// record anything, so counting on the way out would let a row that reliably
// kills the process be retried forever. Counting on the way in bounds it.
//
// Select and update run in one transaction so a second worker cannot claim the
// same row; SQLite serializes writers, so the guard is the status/attempts
// predicate on the UPDATE rather than an explicit lock.
func (r *SQLitePhotoRepository) ClaimNextForProcessing(ctx context.Context, maxAttempts int) (ActivityPhoto, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ActivityPhoto{}, false, err
	}
	defer tx.Rollback() // no-op once Commit has run

	var photoID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM activity_photo
		WHERE status = ? AND attempts < ? AND deleted_at IS NULL
		ORDER BY created_at, id
		LIMIT 1
	`, PhotoStatusProcessing, maxAttempts).Scan(&photoID)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivityPhoto{}, false, nil
	}
	if err != nil {
		return ActivityPhoto{}, false, err
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE activity_photo
		SET attempts = attempts + 1, updated_at = ?
		WHERE id = ? AND status = ? AND attempts < ?
	`, r.now().UTC(), photoID, PhotoStatusProcessing, maxAttempts)
	if err != nil {
		return ActivityPhoto{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ActivityPhoto{}, false, err
	}
	if n == 0 {
		// Another worker got there first. Not an error — just nothing to do.
		return ActivityPhoto{}, false, nil
	}

	row := tx.QueryRowContext(ctx, `
		SELECT `+photoColumns+`
		FROM activity_photo p
		WHERE p.id = ?
	`, photoID)
	p, err := scanPhoto(row)
	if err != nil {
		return ActivityPhoto{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ActivityPhoto{}, false, err
	}
	return p, true, nil
}

// MarkReady records the objects the worker wrote and makes the row renderable.
// This is the only place s3_key and thumb_s3_key become non-empty on the
// two-phase path, and it clears upload_s3_key because the staged original has
// been deleted by then — leaving the key set would name an object that no
// longer exists.
func (r *SQLitePhotoRepository) MarkReady(ctx context.Context, photoID, s3Key, thumbKey string, byteSize int64, width, height int) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET status = ?, s3_key = ?, thumb_s3_key = ?, byte_size = ?,
		    width = ?, height = ?, upload_s3_key = NULL, last_error = NULL,
		    updated_at = ?
		WHERE id = ? AND status = ? AND deleted_at IS NULL
	`, PhotoStatusReady, s3Key, thumbKey, byteSize, width, height,
		r.now().UTC(), photoID, PhotoStatusProcessing)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// MarkFailed retires a row the worker gave up on, keeping the reason for
// diagnosis. The row stays out of every read; nothing surfaces it to the user
// today, which is called out as an open question in the SOW.
func (r *SQLitePhotoRepository) MarkFailed(ctx context.Context, photoID, reason string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET status = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND status = ? AND deleted_at IS NULL
	`, PhotoStatusFailed, reason, r.now().UTC(), photoID, PhotoStatusProcessing)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// RecordAttemptError stores the reason for a transient failure without
// changing status, so the row is retried on a later tick until attempts hits
// the cap.
func (r *SQLitePhotoRepository) RecordAttemptError(ctx context.Context, photoID, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET last_error = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, reason, r.now().UTC(), photoID)
	return err
}

// ExpiredPending returns reservations still awaiting an upload that started
// before cutoff — the rows whose presigned PUT has expired unused, because the
// user cancelled, navigated away, or lost the connection.
func (r *SQLitePhotoRepository) ExpiredPending(ctx context.Context, cutoff time.Time) ([]ActivityPhoto, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+photoColumns+`
		FROM activity_photo p
		WHERE p.status = ? AND p.deleted_at IS NULL AND p.created_at < ?
		ORDER BY p.created_at
	`, PhotoStatusPending, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActivityPhoto
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SoftDeleteByID retires a row by id alone, without an owner or parent check.
// The callers are the reaper and the commit path's cleanup, neither of which
// has a request user in hand. Mirrors the video path's method of the same name.
func (r *SQLitePhotoRepository) SoftDeleteByID(ctx context.Context, photoID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, r.now().UTC(), r.now().UTC(), photoID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// requireOneRow maps "the guarded UPDATE matched nothing" to ErrNotFound, so
// callers can tell a lost race or a replayed request from a real failure.
func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
