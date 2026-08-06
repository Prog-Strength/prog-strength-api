package activity

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/id"
)

// Video lifecycle states. A row is created 'pending' when the client reserves
// an upload slot and flips to 'ready' once the commit call confirms the object
// exists in S3. Only 'ready' rows are ever returned by reads — a pending row
// describes an upload that may never have happened.
const (
	VideoStatusPending = "pending"
	VideoStatusReady   = "ready"
)

// ActivityVideo is one uploaded video attached to an activity.
//
// PosterS3Key, DurationSeconds, Width, and Height are all nullable, which is
// the shape difference from ActivityPhoto worth internalizing: the poster is
// generated client-side and can legitimately fail, and the dimension/duration
// values are client-reported because nothing server-side decodes the container.
// They are display hints. ByteSize is NOT a hint — it is re-derived from S3 by
// a HEAD at commit and is the value the size cap is enforced against.
type ActivityVideo struct {
	ID              string
	ActivityID      string
	UserID          string
	S3Key           string
	PosterS3Key     *string
	ContentType     string
	ByteSize        int64
	DurationSeconds *float64
	Width           *int
	Height          *int
	Status          string
	Caption         *string
	Position        int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// VideoRepository persists activity videos. Reads require the PARENT activity
// to be live and enforce ownership at the storage layer, mirroring
// PhotoRepository's ErrNotFound-on-wrong-user contract.
type VideoRepository interface {
	// Insert reserves a video: writes a 'pending' row, assigning
	// position = COALESCE(MAX(position), -1) + 1 over that activity's LIVE
	// rows, generating the id, and stamping created_at/updated_at.
	Insert(ctx context.Context, v ActivityVideo) (ActivityVideo, error)

	// MarkReady flips a pending row to 'ready', recording the S3-confirmed
	// byte size and the poster key (nil when poster generation failed).
	// Returns ErrNotFound when no live owned PENDING row matches — committing
	// twice is therefore not idempotent by accident, it's a miss.
	MarkReady(ctx context.Context, userID, activityID, videoID string, byteSize int64, posterKey *string) error

	// ListByActivity returns the user's live READY videos on the given LIVE
	// activity, ordered (position, id). Pending rows are invisible.
	ListByActivity(ctx context.Context, userID, activityID string) ([]ActivityVideo, error)

	// CountLive counts live videos on an activity, INCLUDING pending ones —
	// an in-flight upload must occupy a slot or the per-activity cap could be
	// blown by racing reservations.
	CountLive(ctx context.Context, activityID string) (int, error)

	// Get returns one live video for its owner on a live activity, in any
	// status. Returns ErrNotFound when missing, soft-deleted, under a
	// soft-deleted parent, or owned by another user.
	Get(ctx context.Context, userID, activityID, videoID string) (ActivityVideo, error)

	// UpdateCaption sets (or clears, with nil) a live owned video's caption.
	UpdateCaption(ctx context.Context, userID, activityID, videoID string, caption *string) error

	// SoftDelete stamps deleted_at on a live owned video.
	SoftDelete(ctx context.Context, userID, activityID, videoID string) error

	// SetS3Key records the object key for a reserved video. The key embeds the
	// row's own id, so it can only be computed AFTER Insert — hence the second
	// write rather than a field on Insert.
	SetS3Key(ctx context.Context, videoID, key string) error

	// LiveIDs returns the ids of the user's live READY videos on the activity,
	// ordered (position, id). Backs the reorder handler's full-set check.
	LiveIDs(ctx context.Context, userID, activityID string) ([]string, error)

	// Reorder rewrites each id's position to its index in orderedIDs, in one
	// transaction, scoped to the user's live ready videos on the activity.
	Reorder(ctx context.Context, userID, activityID string, orderedIDs []string) error

	// ListStalePending returns pending rows created before cutoff, across all
	// users — the reaper's input. Returns whole rows so the caller can tag
	// their S3 objects before soft-deleting them.
	ListStalePending(ctx context.Context, cutoff time.Time) ([]ActivityVideo, error)

	// SoftDeleteByID stamps deleted_at on a row by id alone, without an
	// ownership filter. The reaper runs outside any request and has already
	// selected exactly the rows it means to retire.
	SoftDeleteByID(ctx context.Context, videoID string) error
}

// SQLiteVideoRepository is the production VideoRepository. It holds its own
// *sql.DB so it composes cleanly beside SQLitePhotoRepository.
type SQLiteVideoRepository struct {
	db  *sql.DB
	now func() time.Time
}

// Compile-time check that *SQLiteVideoRepository satisfies VideoRepository.
var _ VideoRepository = (*SQLiteVideoRepository)(nil)

func NewSQLiteVideoRepository(db *sql.DB) *SQLiteVideoRepository {
	return &SQLiteVideoRepository{db: db, now: time.Now}
}

// videoColumns is the canonical select list for a full ActivityVideo row, kept
// in one place so scan order can't drift between queries.
const videoColumns = `v.id, v.activity_id, v.user_id, v.s3_key, v.poster_s3_key,
	v.content_type, v.byte_size, v.duration_seconds, v.width, v.height,
	v.status, v.caption, v.position, v.created_at, v.updated_at, v.deleted_at`

// scanVideo reads one activity_video row (projected via videoColumns).
func scanVideo(s interface{ Scan(...any) error }) (ActivityVideo, error) {
	var (
		v         ActivityVideo
		posterKey sql.NullString
		duration  sql.NullFloat64
		width     sql.NullInt64
		height    sql.NullInt64
		caption   sql.NullString
		deletedAt sql.NullTime
	)
	if err := s.Scan(
		&v.ID, &v.ActivityID, &v.UserID, &v.S3Key, &posterKey,
		&v.ContentType, &v.ByteSize, &duration, &width, &height,
		&v.Status, &caption, &v.Position, &v.CreatedAt, &v.UpdatedAt, &deletedAt,
	); err != nil {
		return ActivityVideo{}, err
	}
	if posterKey.Valid {
		s := posterKey.String
		v.PosterS3Key = &s
	}
	if duration.Valid {
		d := duration.Float64
		v.DurationSeconds = &d
	}
	if width.Valid {
		w := int(width.Int64)
		v.Width = &w
	}
	if height.Valid {
		h := int(height.Int64)
		v.Height = &h
	}
	if caption.Valid {
		c := caption.String
		v.Caption = &c
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		v.DeletedAt = &t
	}
	return v, nil
}

func (r *SQLiteVideoRepository) Insert(ctx context.Context, v ActivityVideo) (ActivityVideo, error) {
	now := r.now().UTC()
	v.ID = id.New()
	v.CreatedAt = now
	v.UpdatedAt = now
	v.DeletedAt = nil
	v.Status = VideoStatusPending

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ActivityVideo{}, err
	}
	defer tx.Rollback()

	// Positions stay dense over LIVE rows (pending included — a reservation
	// holds its slot). Empty case yields -1 + 1 = 0.
	var pos int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(position), -1) + 1
		FROM activity_video
		WHERE activity_id = ? AND deleted_at IS NULL
	`, v.ActivityID).Scan(&pos); err != nil {
		return ActivityVideo{}, err
	}
	v.Position = pos

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO activity_video (
			id, activity_id, user_id, s3_key, poster_s3_key, content_type,
			byte_size, duration_seconds, width, height, status, caption,
			position, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, v.ID, v.ActivityID, v.UserID, v.S3Key, v.PosterS3Key, v.ContentType,
		v.ByteSize, v.DurationSeconds, v.Width, v.Height, v.Status, v.Caption,
		v.Position, v.CreatedAt, v.UpdatedAt); err != nil {
		return ActivityVideo{}, err
	}

	if err := tx.Commit(); err != nil {
		return ActivityVideo{}, err
	}
	return v, nil
}

func (r *SQLiteVideoRepository) MarkReady(ctx context.Context, userID, activityID, videoID string, byteSize int64, posterKey *string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_video
		SET status = ?, byte_size = ?, poster_s3_key = ?, updated_at = ?
		WHERE id = ? AND activity_id = ? AND user_id = ?
		  AND status = ? AND deleted_at IS NULL
	`, VideoStatusReady, byteSize, posterKey, r.now().UTC(),
		videoID, activityID, userID, VideoStatusPending)
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

func (r *SQLiteVideoRepository) ListByActivity(ctx context.Context, userID, activityID string) ([]ActivityVideo, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+videoColumns+`
		FROM activity_video v
		JOIN activities a ON a.id = v.activity_id
		WHERE v.activity_id = ? AND v.user_id = ?
		  AND v.status = ?
		  AND v.deleted_at IS NULL AND a.deleted_at IS NULL
		ORDER BY v.position, v.id
	`, activityID, userID, VideoStatusReady)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActivityVideo
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *SQLiteVideoRepository) CountLive(ctx context.Context, activityID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM activity_video
		WHERE activity_id = ? AND deleted_at IS NULL
	`, activityID).Scan(&n)
	return n, err
}

func (r *SQLiteVideoRepository) Get(ctx context.Context, userID, activityID, videoID string) (ActivityVideo, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+videoColumns+`
		FROM activity_video v
		JOIN activities a ON a.id = v.activity_id
		WHERE v.id = ? AND v.activity_id = ? AND v.user_id = ?
		  AND v.deleted_at IS NULL AND a.deleted_at IS NULL
	`, videoID, activityID, userID)
	v, err := scanVideo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivityVideo{}, ErrNotFound
	}
	return v, err
}

func (r *SQLiteVideoRepository) UpdateCaption(ctx context.Context, userID, activityID, videoID string, caption *string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_video
		SET caption = ?, updated_at = ?
		WHERE id = ? AND activity_id = ? AND user_id = ? AND deleted_at IS NULL
	`, caption, r.now().UTC(), videoID, activityID, userID)
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

func (r *SQLiteVideoRepository) SoftDelete(ctx context.Context, userID, activityID, videoID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_video
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND activity_id = ? AND user_id = ? AND deleted_at IS NULL
	`, now, now, videoID, activityID, userID)
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

func (r *SQLiteVideoRepository) SetS3Key(ctx context.Context, videoID, key string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE activity_video
		SET s3_key = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, key, r.now().UTC(), videoID)
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

func (r *SQLiteVideoRepository) LiveIDs(ctx context.Context, userID, activityID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT v.id
		FROM activity_video v
		JOIN activities a ON a.id = v.activity_id
		WHERE v.activity_id = ? AND v.user_id = ?
		  AND v.status = ?
		  AND v.deleted_at IS NULL AND a.deleted_at IS NULL
		ORDER BY v.position, v.id
	`, activityID, userID, VideoStatusReady)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var vid string
		if err := rows.Scan(&vid); err != nil {
			return nil, err
		}
		out = append(out, vid)
	}
	return out, rows.Err()
}

func (r *SQLiteVideoRepository) Reorder(ctx context.Context, userID, activityID string, orderedIDs []string) error {
	if len(orderedIDs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := r.now().UTC()
	for i, vid := range orderedIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE activity_video
			SET position = ?, updated_at = ?
			WHERE id = ? AND activity_id = ? AND user_id = ? AND deleted_at IS NULL
		`, i, now, vid, activityID, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLiteVideoRepository) ListStalePending(ctx context.Context, cutoff time.Time) ([]ActivityVideo, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+videoColumns+`
		FROM activity_video v
		WHERE v.status = ? AND v.deleted_at IS NULL AND v.created_at < ?
		ORDER BY v.created_at
	`, VideoStatusPending, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActivityVideo
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *SQLiteVideoRepository) SoftDeleteByID(ctx context.Context, videoID string) error {
	now := r.now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE activity_video
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, now, now, videoID)
	return err
}
