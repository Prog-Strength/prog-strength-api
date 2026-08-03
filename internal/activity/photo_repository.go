package activity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// ActivityPhoto is one uploaded photo attached to an activity, with its full
// and thumbnail S3 variant keys plus display metadata. Caption and DeletedAt
// are nullable; DeletedAt == nil means the photo is live. Positions are 0-based
// and dense within an activity (Insert appends, Reorder rewrites the whole set).
type ActivityPhoto struct {
	ID          string
	ActivityID  string
	UserID      string
	S3Key       string
	ThumbS3Key  string
	ContentType string
	ByteSize    int64
	Width       int
	Height      int
	Caption     *string
	Position    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time

	// Status is the three-phase upload's state — see PhotoStatus* below.
	// Rows written by the retired synchronous path are 'ready' by DEFAULT.
	Status string
	// UploadS3Key is the STAGING object the client PUT to. It still carries
	// the source's EXIF (including GPS), so it is never presigned for GET and
	// is deleted once the worker has written the stripped copy. Nil on rows
	// from the synchronous path, which never staged anything.
	UploadS3Key *string
	// Attempts counts transient processing failures, bounding retries so a
	// poison image cannot be retried forever. LastError records the final one.
	Attempts  int
	LastError *string
}

// Photo upload states. Mirrors VideoStatus* in video_repository.go, with an
// extra pair: a photo has real work to do after its bytes land, so it needs a
// state for "the worker holds it" and one for "the worker gave up".
const (
	// PhotoStatusPending — reserved; the client is uploading to S3. Excluded
	// from every read: a reservation is not a photo.
	PhotoStatusPending = "pending"
	// PhotoStatusProcessing — the object landed and the worker holds it.
	// Included in the detail read so the strip can show a placeholder in the
	// right position, but never resolved to URLs.
	PhotoStatusProcessing = "processing"
	// PhotoStatusReady — stripped photo and thumb written; renderable.
	PhotoStatusReady = "ready"
	// PhotoStatusFailed — the worker gave up. Excluded from reads; visible in
	// last_error and the fallback/failure metrics.
	PhotoStatusFailed = "failed"
)

// PhotoCover is a single activity's cover photo (lowest position, id tiebreak)
// plus the total live-photo count, as returned by CoverPhotosByActivityIDs for
// the timeline's per-activity photo strip.
type PhotoCover struct {
	Cover ActivityPhoto
	Count int
}

// PhotoRepository persists activity photos. Reads require the PARENT activity
// to be live (a soft-deleted activity hides its photos even though the photo
// rows are untouched) and enforce ownership at the storage layer, mirroring
// Repository's ErrNotFound-on-wrong-user contract.
type PhotoRepository interface {
	// Insert appends a photo to its activity, assigning
	// position = COALESCE(MAX(position), -1) + 1 over that activity's LIVE
	// rows, generating the id, and stamping created_at/updated_at. Returns the
	// stored row with those fields populated.
	Insert(ctx context.Context, p ActivityPhoto) (ActivityPhoto, error)

	// ListByActivity returns the user's live photos on the given LIVE activity,
	// ordered (position, id). Returns nothing when the parent is soft-deleted.
	ListByActivity(ctx context.Context, userID, activityID string) ([]ActivityPhoto, error)

	// CountLive counts live photos on an activity (scoped by activity_id +
	// deleted_at IS NULL only — no parent-live or ownership filter, since the
	// caller has already resolved the owned live activity for its upload-cap
	// check).
	CountLive(ctx context.Context, activityID string) (int, error)

	// Get returns one live photo for its owner on a live activity. Returns
	// ErrNotFound when missing, soft-deleted, under a soft-deleted parent, or
	// owned by another user.
	Get(ctx context.Context, userID, activityID, photoID string) (ActivityPhoto, error)

	// UpdateCaption sets (or, with nil, clears) a live owned photo's caption and
	// bumps updated_at. Returns ErrNotFound when no live owned row matches.
	UpdateCaption(ctx context.Context, userID, activityID, photoID string, caption *string) error

	// SoftDelete stamps deleted_at on a live owned photo. Returns ErrNotFound
	// when no live owned row matches.
	SoftDelete(ctx context.Context, userID, activityID, photoID string) error

	// Reorder rewrites each id's position to its index in orderedIDs, in one
	// transaction, scoped to the user's live photos on the activity. The
	// handler validates the full-set match before calling; the repo just
	// rewrites.
	Reorder(ctx context.Context, userID, activityID string, orderedIDs []string) error

	// LiveIDs returns the ids of the user's live photos on the activity,
	// ordered (position, id) — the full set the reorder handler validates
	// against.
	LiveIDs(ctx context.Context, userID, activityID string) ([]string, error)

	// CoverPhotosByActivityIDs returns, per activity in ids, its cover photo
	// (lowest position, id tiebreak) and live-photo count in a SINGLE query.
	// Activities with no live photos, or whose parent is soft-deleted, are
	// absent. No ownership scoping — the timeline spans multiple authors.
	// Restricted to 'ready': a photo the worker still holds has no URLs to
	// resolve, so letting it win the cover slot would blank a feed post.
	CoverPhotosByActivityIDs(ctx context.Context, activityIDs []string) (map[string]PhotoCover, error)

	// --- two-phase upload (see photo_pending_repository.go) ---------------

	// SetUploadKey records the staging object key on a reserved row.
	SetUploadKey(ctx context.Context, photoID, key string) error

	// MarkProcessing flips a reservation to 'processing' once commit has
	// confirmed the object landed, recording its S3-confirmed size. Returns
	// ErrNotFound when the row is not a live pending reservation of this
	// owner, which is how a replayed commit is rejected.
	MarkProcessing(ctx context.Context, userID, activityID, photoID string, byteSize int64) error

	// ClaimNextForProcessing takes the oldest row still under the attempt cap
	// and increments its counter, returning ok=false when there is nothing to
	// do. Incrementing at claim time (not on failure) is what bounds a photo
	// that kills the worker before it can record anything.
	ClaimNextForProcessing(ctx context.Context, maxAttempts int) (ActivityPhoto, bool, error)

	// MarkReady records the objects the worker wrote, clears upload_s3_key
	// (the staged original is gone by then) and makes the row renderable.
	MarkReady(ctx context.Context, photoID, s3Key, thumbKey string, byteSize int64, width, height int) error

	// MarkFailed retires a row the worker gave up on, keeping the reason.
	MarkFailed(ctx context.Context, photoID, reason string) error

	// RecordAttemptError stores a transient failure's reason without changing
	// status, so the row is retried until attempts hits the cap.
	RecordAttemptError(ctx context.Context, photoID, reason string) error

	// ExpiredPending returns reservations created before cutoff that never
	// received their upload — the reaper's input.
	ExpiredPending(ctx context.Context, cutoff time.Time) ([]ActivityPhoto, error)

	// SoftDeleteByID retires a row by id alone, for callers (the reaper, the
	// commit cleanup path) that have no request user in hand.
	SoftDeleteByID(ctx context.Context, photoID string) error
}

// SQLitePhotoRepository is the SQLite-backed PhotoRepository. It holds only a
// *sql.DB so it composes cleanly beside SQLiteRepository and is trivial to fake.
type SQLitePhotoRepository struct {
	db  *sql.DB
	now func() time.Time
}

// Compile-time check that *SQLitePhotoRepository satisfies PhotoRepository.
var _ PhotoRepository = (*SQLitePhotoRepository)(nil)

func NewSQLitePhotoRepository(db *sql.DB) *SQLitePhotoRepository {
	return &SQLitePhotoRepository{db: db, now: time.Now}
}

// photoColumns is the canonical select list for a full ActivityPhoto row,
// kept in one place so scan order can't drift between queries.
const photoColumns = `p.id, p.activity_id, p.user_id, p.s3_key, p.thumb_s3_key,
	p.content_type, p.byte_size, p.width, p.height, p.caption, p.position,
	p.created_at, p.updated_at, p.deleted_at,
	p.status, p.upload_s3_key, p.attempts, p.last_error`

// photoVisible is the status filter for reads that feed the photo strip: a
// finished photo, or one the worker still holds (which the client renders as a
// placeholder in its final position). A pending reservation is not a photo
// yet, and a failed render never will be, so neither is ever returned.
//
// Timeline covers deliberately do NOT use this — see CoverPhotosByActivityIDs.
const photoVisible = `p.status IN ('ready', 'processing')`

// scanPhoto reads one activity_photo row (projected via photoColumns) out of a
// Row or Rows.
func scanPhoto(s interface{ Scan(...any) error }) (ActivityPhoto, error) {
	var (
		p         ActivityPhoto
		caption   sql.NullString
		deletedAt sql.NullTime
		uploadKey sql.NullString
		lastError sql.NullString
	)
	if err := s.Scan(
		&p.ID, &p.ActivityID, &p.UserID, &p.S3Key, &p.ThumbS3Key,
		&p.ContentType, &p.ByteSize, &p.Width, &p.Height, &caption, &p.Position,
		&p.CreatedAt, &p.UpdatedAt, &deletedAt,
		&p.Status, &uploadKey, &p.Attempts, &lastError,
	); err != nil {
		return ActivityPhoto{}, err
	}
	if uploadKey.Valid {
		v := uploadKey.String
		p.UploadS3Key = &v
	}
	if lastError.Valid {
		v := lastError.String
		p.LastError = &v
	}
	if caption.Valid {
		v := caption.String
		p.Caption = &v
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		p.DeletedAt = &t
	}
	return p, nil
}

func (r *SQLitePhotoRepository) Insert(ctx context.Context, p ActivityPhoto) (ActivityPhoto, error) {
	now := r.now().UTC()
	p.ID = id.New()
	p.CreatedAt = now
	p.UpdatedAt = now
	p.DeletedAt = nil
	if p.Status == "" {
		// The synchronous upload path writes a finished row and knows nothing
		// about states. Defaulting here rather than at the call site is what
		// lets that path stay mounted unchanged through the transition.
		p.Status = PhotoStatusReady
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ActivityPhoto{}, err
	}
	defer tx.Rollback()

	// position = MAX(position)+1 over the activity's LIVE rows keeps positions
	// dense; the empty case (no live rows) yields -1 + 1 = 0.
	var pos int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(position), -1) + 1
		FROM activity_photo
		WHERE activity_id = ? AND deleted_at IS NULL
	`, p.ActivityID).Scan(&pos); err != nil {
		return ActivityPhoto{}, err
	}
	p.Position = pos

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO activity_photo (
			id, activity_id, user_id, s3_key, thumb_s3_key, content_type,
			byte_size, width, height, caption, position, created_at, updated_at,
			status, upload_s3_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.ActivityID, p.UserID, p.S3Key, p.ThumbS3Key, p.ContentType,
		p.ByteSize, p.Width, p.Height, p.Caption, p.Position, p.CreatedAt, p.UpdatedAt,
		p.Status, p.UploadS3Key); err != nil {
		return ActivityPhoto{}, err
	}

	if err := tx.Commit(); err != nil {
		return ActivityPhoto{}, err
	}
	return p, nil
}

func (r *SQLitePhotoRepository) ListByActivity(ctx context.Context, userID, activityID string) ([]ActivityPhoto, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+photoColumns+`
		FROM activity_photo p
		JOIN activities a ON a.id = p.activity_id
		WHERE p.activity_id = ? AND p.user_id = ?
		  AND p.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND `+photoVisible+`
		ORDER BY p.position, p.id
	`, activityID, userID)
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

func (r *SQLitePhotoRepository) CountLive(ctx context.Context, activityID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM activity_photo
		WHERE activity_id = ? AND deleted_at IS NULL
		  AND status <> 'failed'
	`, activityID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (r *SQLitePhotoRepository) Get(ctx context.Context, userID, activityID, photoID string) (ActivityPhoto, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+photoColumns+`
		FROM activity_photo p
		JOIN activities a ON a.id = p.activity_id
		WHERE p.id = ? AND p.activity_id = ? AND p.user_id = ?
		  AND p.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND `+photoVisible+`
	`, photoID, activityID, userID)
	p, err := scanPhoto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivityPhoto{}, ErrNotFound
	}
	if err != nil {
		return ActivityPhoto{}, err
	}
	return p, nil
}

func (r *SQLitePhotoRepository) UpdateCaption(ctx context.Context, userID, activityID, photoID string, caption *string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET caption = ?, updated_at = ?
		WHERE id = ? AND activity_id = ? AND user_id = ? AND deleted_at IS NULL
	`, caption, r.now().UTC(), photoID, activityID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLitePhotoRepository) SoftDelete(ctx context.Context, userID, activityID, photoID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_photo
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND activity_id = ? AND user_id = ? AND deleted_at IS NULL
	`, r.now().UTC(), r.now().UTC(), photoID, activityID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLitePhotoRepository) Reorder(ctx context.Context, userID, activityID string, orderedIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE activity_photo
		SET position = ?, updated_at = ?
		WHERE id = ? AND activity_id = ? AND user_id = ? AND deleted_at IS NULL
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for pos, photoID := range orderedIDs {
		if _, err := stmt.ExecContext(ctx, pos, now, photoID, activityID, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLitePhotoRepository) LiveIDs(ctx context.Context, userID, activityID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id
		FROM activity_photo p
		JOIN activities a ON a.id = p.activity_id
		WHERE p.activity_id = ? AND p.user_id = ?
		  AND p.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND `+photoVisible+`
		ORDER BY p.position, p.id
	`, activityID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var photoID string
		if err := rows.Scan(&photoID); err != nil {
			return nil, err
		}
		out = append(out, photoID)
	}
	return out, rows.Err()
}

func (r *SQLitePhotoRepository) CoverPhotosByActivityIDs(ctx context.Context, activityIDs []string) (map[string]PhotoCover, error) {
	out := make(map[string]PhotoCover, len(activityIDs))
	if len(activityIDs) == 0 {
		return out, nil
	}

	// Parameterized IN list: placeholders only, ids passed as variadic args —
	// never interpolated into SQL.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(activityIDs)), ",")
	args := make([]any, 0, len(activityIDs))
	for _, aid := range activityIDs {
		args = append(args, aid)
	}

	// One query: a CTE numbers each activity's live photos by (position, id)
	// and counts them per activity via a window COUNT; the outer select keeps
	// rn = 1 (the cover) and carries the per-activity count alongside it. The
	// activities JOIN enforces parent-live; a soft-deleted parent contributes
	// no ranked rows and is therefore absent.
	rows, err := r.db.QueryContext(ctx, `
		WITH ranked AS (
			SELECT `+photoColumns+`,
			       ROW_NUMBER() OVER (PARTITION BY p.activity_id ORDER BY p.position, p.id) AS rn,
			       COUNT(*)     OVER (PARTITION BY p.activity_id) AS cnt
			FROM activity_photo p
			JOIN activities a ON a.id = p.activity_id
			WHERE p.deleted_at IS NULL AND a.deleted_at IS NULL
			  AND p.status = 'ready'
			  AND p.activity_id IN (`+placeholders+`)
		)
		SELECT `+photoColumns+`, cnt
		FROM ranked p
		WHERE rn = 1
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			p         ActivityPhoto
			caption   sql.NullString
			deletedAt sql.NullTime
			uploadKey sql.NullString
			lastError sql.NullString
			count     int
		)
		if err := rows.Scan(
			&p.ID, &p.ActivityID, &p.UserID, &p.S3Key, &p.ThumbS3Key,
			&p.ContentType, &p.ByteSize, &p.Width, &p.Height, &caption, &p.Position,
			&p.CreatedAt, &p.UpdatedAt, &deletedAt,
			&p.Status, &uploadKey, &p.Attempts, &lastError, &count,
		); err != nil {
			return nil, err
		}
		if caption.Valid {
			v := caption.String
			p.Caption = &v
		}
		if deletedAt.Valid {
			t := deletedAt.Time
			p.DeletedAt = &t
		}
		out[p.ActivityID] = PhotoCover{Cover: p, Count: count}
	}
	return out, rows.Err()
}
