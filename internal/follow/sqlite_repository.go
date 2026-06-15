package follow

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// followColumns is the canonical select list, kept in one place so scan order
// can't drift between queries.
const followColumns = `
	id, follower_id, followee_id, status, created_at, accepted_at`

type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

// Request inserts a pending edge follower → followee. Self-follow is rejected
// before touching storage; a duplicate ordered pair surfaces as the UNIQUE
// constraint, mapped to ErrAlreadyExists; the pending cap is checked first so a
// spammer is stopped before the insert.
func (r *SQLiteRepository) Request(ctx context.Context, followerID, followeeID string) (Follow, error) {
	if followerID == followeeID {
		return Follow{}, ErrSelfFollow
	}

	pending, err := r.CountPending(ctx, followerID)
	if err != nil {
		return Follow{}, err
	}
	if pending >= PendingCap {
		return Follow{}, ErrPendingCapExceeded
	}

	now := r.now().UTC()
	f := Follow{
		ID:         id.New(),
		FollowerID: followerID,
		FolloweeID: followeeID,
		Status:     StatusPending,
		CreatedAt:  now,
	}
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO follows (id, follower_id, followee_id, status, created_at, accepted_at)
		VALUES (?, ?, ?, ?, ?, NULL)
	`, f.ID, f.FollowerID, f.FolloweeID, string(f.Status), f.CreatedAt); err != nil {
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			return Follow{}, ErrAlreadyExists
		}
		return Follow{}, err
	}
	return f, nil
}

// Accept flips the pending row addressed to followeeID to accepted.
func (r *SQLiteRepository) Accept(ctx context.Context, followeeID, followerID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE follows
		SET status = ?, accepted_at = ?
		WHERE follower_id = ? AND followee_id = ? AND status = ?
	`, string(StatusAccepted), now, followerID, followeeID, string(StatusPending))
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

// Reject deletes the pending row addressed to followeeID.
func (r *SQLiteRepository) Reject(ctx context.Context, followeeID, followerID string) error {
	return r.deleteOne(ctx, followerID, followeeID, StatusPending)
}

// Cancel deletes the requester's own pending row.
func (r *SQLiteRepository) Cancel(ctx context.Context, followerID, followeeID string) error {
	return r.deleteOne(ctx, followerID, followeeID, StatusPending)
}

// Unfollow deletes the requester's own accepted row.
func (r *SQLiteRepository) Unfollow(ctx context.Context, followerID, followeeID string) error {
	return r.deleteOne(ctx, followerID, followeeID, StatusAccepted)
}

// RemoveFollower deletes the accepted row where the actor is the followee.
func (r *SQLiteRepository) RemoveFollower(ctx context.Context, followeeID, followerID string) error {
	return r.deleteOne(ctx, followerID, followeeID, StatusAccepted)
}

// deleteOne deletes the row for the ordered pair in the given status, returning
// ErrNotFound when nothing matched. All four delete transitions reduce to this:
// the (follower, followee, status) triple is the full authorization predicate.
func (r *SQLiteRepository) deleteOne(ctx context.Context, followerID, followeeID string, status Status) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM follows
		WHERE follower_id = ? AND followee_id = ? AND status = ?
	`, followerID, followeeID, string(status))
	if err != nil {
		return err
	}
	return errIfNoRows(res)
}

// Get returns the edge for the ordered pair regardless of status.
func (r *SQLiteRepository) Get(ctx context.Context, followerID, followeeID string) (Follow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+followColumns+`
		FROM follows
		WHERE follower_id = ? AND followee_id = ?
	`, followerID, followeeID)
	f, err := scanFollow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Follow{}, ErrNotFound
	}
	if err != nil {
		return Follow{}, err
	}
	return *f, nil
}

func (r *SQLiteRepository) CountPending(ctx context.Context, followerID string) (int, error) {
	return r.count(ctx, `follower_id = ? AND status = ?`, followerID, string(StatusPending))
}

func (r *SQLiteRepository) CountFollowers(ctx context.Context, userID string) (int, error) {
	return r.count(ctx, `followee_id = ? AND status = ?`, userID, string(StatusAccepted))
}

func (r *SQLiteRepository) CountFollowing(ctx context.Context, userID string) (int, error) {
	return r.count(ctx, `follower_id = ? AND status = ?`, userID, string(StatusAccepted))
}

func (r *SQLiteRepository) count(ctx context.Context, where string, args ...any) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM follows WHERE `+where, args...).Scan(&n)
	return n, err
}

// AcceptedFollowees returns the followee ids of viewerID's accepted edges.
func (r *SQLiteRepository) AcceptedFollowees(ctx context.Context, viewerID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT followee_id
		FROM follows
		WHERE follower_id = ? AND status = ?
		ORDER BY created_at DESC, id DESC
	`, viewerID, string(StatusAccepted))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Relationship returns viewerID's relationship to otherID.
func (r *SQLiteRepository) Relationship(ctx context.Context, viewerID, otherID string) (Relationship, error) {
	if viewerID == otherID {
		return RelationshipSelf, nil
	}
	// Both directions in one query; the handler-level pair is small.
	rows, err := r.db.QueryContext(ctx, `
		SELECT follower_id, followee_id, status
		FROM follows
		WHERE (follower_id = ? AND followee_id = ?) OR (follower_id = ? AND followee_id = ?)
	`, viewerID, otherID, otherID, viewerID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	rel := RelationshipNone
	for rows.Next() {
		var followerID, followeeID, status string
		if err := rows.Scan(&followerID, &followeeID, &status); err != nil {
			return "", err
		}
		rel = mergeRelationship(rel, viewerID, followerID, Status(status))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return rel, nil
}

// Relationships batch-computes viewerID's relationship to each id in otherIDs.
func (r *SQLiteRepository) Relationships(ctx context.Context, viewerID string, otherIDs []string) (map[string]Relationship, error) {
	out := make(map[string]Relationship, len(otherIDs))
	for _, oid := range otherIDs {
		if oid == viewerID {
			out[oid] = RelationshipSelf
		} else {
			out[oid] = RelationshipNone
		}
	}
	if len(otherIDs) == 0 {
		return out, nil
	}

	placeholders, idArgs := inPlaceholders(otherIDs)
	// Any edge touching the viewer and one of the requested ids, in either
	// direction. One query for the whole page.
	args := append([]any{viewerID}, idArgs...)
	args = append(args, viewerID)
	args = append(args, idArgs...)
	rows, err := r.db.QueryContext(ctx, `
		SELECT follower_id, followee_id, status
		FROM follows
		WHERE (follower_id = ? AND followee_id IN (`+placeholders+`))
		   OR (followee_id = ? AND follower_id IN (`+placeholders+`))
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var followerID, followeeID, status string
		if err := rows.Scan(&followerID, &followeeID, &status); err != nil {
			return nil, err
		}
		other := followeeID
		if followerID != viewerID {
			other = followerID
		}
		if _, ok := out[other]; !ok {
			continue // edge to an id we weren't asked about; ignore
		}
		out[other] = mergeRelationship(out[other], viewerID, followerID, Status(status))
	}
	return out, rows.Err()
}

func (r *SQLiteRepository) ListFollowers(ctx context.Context, followeeID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(ctx, `followee_id = ? AND status = ?`, []any{followeeID, string(StatusAccepted)}, limit, before)
}

func (r *SQLiteRepository) ListFollowing(ctx context.Context, followerID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(ctx, `follower_id = ? AND status = ?`, []any{followerID, string(StatusAccepted)}, limit, before)
}

func (r *SQLiteRepository) ListIncomingRequests(ctx context.Context, followeeID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(ctx, `followee_id = ? AND status = ?`, []any{followeeID, string(StatusPending)}, limit, before)
}

func (r *SQLiteRepository) ListOutgoingRequests(ctx context.Context, followerID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(ctx, `follower_id = ? AND status = ?`, []any{followerID, string(StatusPending)}, limit, before)
}

// list runs a keyset-paginated select for the given base predicate, newest-first
// (created_at DESC, id DESC). It fetches limit+1 rows to detect a further page
// and returns a non-nil cursor only when one exists, mirroring timeline.ListFeed.
func (r *SQLiteRepository) list(ctx context.Context, base string, baseArgs []any, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	clauses := []string{base}
	args := append([]any{}, baseArgs...)
	if before != nil {
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, before.CreatedAt.UTC(), before.CreatedAt.UTC(), before.ID)
	}
	q := `
		SELECT ` + followColumns + `
		FROM follows
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY created_at DESC, id DESC
		LIMIT ?`
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	out := make([]Follow, 0)
	for rows.Next() {
		f, err := scanFollow(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		return out, &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
	}
	return out, nil, nil
}

// errIfNoRows turns a zero-rows-affected result into ErrNotFound.
func errIfNoRows(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// inPlaceholders builds a "?,?,..." list and the matching []any args for a
// dynamic IN clause.
func inPlaceholders(ids []string) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, v := range ids {
		ph[i] = "?"
		args[i] = v
	}
	return strings.Join(ph, ","), args
}

// scanFollow reads one follows row out of a Row or Rows.
func scanFollow(s interface{ Scan(...any) error }) (*Follow, error) {
	var (
		f          Follow
		status     string
		acceptedAt sql.NullTime
	)
	if err := s.Scan(&f.ID, &f.FollowerID, &f.FolloweeID, &status, &f.CreatedAt, &acceptedAt); err != nil {
		return nil, err
	}
	f.Status = Status(status)
	if acceptedAt.Valid {
		t := acceptedAt.Time
		f.AcceptedAt = &t
	}
	return &f, nil
}
