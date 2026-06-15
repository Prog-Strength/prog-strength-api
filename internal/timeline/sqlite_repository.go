package timeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

const (
	// maxCommentBodyLen mirrors the domain validation contract: comment
	// bodies are non-empty after trimming and at most this many characters.
	maxCommentBodyLen = 2000
)

// postColumns / commentColumns / reactionColumns are the canonical select
// lists, kept in one place so scan order can't drift between queries.
const postColumns = `
	id, user_id, source_type, source_id, occurred_at, visibility, created_at, updated_at`

const commentColumns = `
	id, post_id, user_id, body, created_at, updated_at, deleted_at`

const reactionColumns = `
	id, post_id, user_id, type, created_at`

type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, now: time.Now}
}

// EnsurePost idempotently inserts the feed-index row for ref and returns it.
// On the UNIQUE(user_id, source_type, source_id) conflict the insert is a
// no-op and the existing row (with its original id/occurred_at) is returned,
// so the live write hook and the backfill share one path.
func (r *SQLiteRepository) EnsurePost(ctx context.Context, ref PostRef) (Post, error) {
	now := r.now().UTC()
	newID := id.New()

	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO timeline_post (
			id, user_id, source_type, source_id, occurred_at, visibility, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, source_type, source_id) DO NOTHING
	`, newID, ref.UserID, ref.SourceType, ref.SourceID, ref.OccurredAt.UTC(),
		string(VisibilityFriends), now, now); err != nil {
		return Post{}, err
	}

	// Re-read by the unique key so a conflict returns the EXISTING row.
	row := r.db.QueryRowContext(ctx, `
		SELECT `+postColumns+`
		FROM timeline_post
		WHERE user_id = ? AND source_type = ? AND source_id = ?
	`, ref.UserID, ref.SourceType, ref.SourceID)
	p, err := scanPost(row)
	if err != nil {
		return Post{}, err
	}
	return *p, nil
}

// ListFeed returns posts authored by any user in userIDs, newest-first
// (occurred_at DESC, id DESC), capped at limit, using a keyset cursor. A post
// is admitted only when it is the viewer's own or its visibility is not
// 'private'. It fetches limit+1 rows to detect a further page and returns a
// non-nil cursor only when one exists. An empty userIDs returns an empty page
// (guarding against an invalid `IN ()`).
func (r *SQLiteRepository) ListFeed(ctx context.Context, userIDs []string, viewerID string, limit int, before *Cursor) ([]Post, *Cursor, error) {
	if len(userIDs) == 0 {
		return nil, nil, nil
	}
	placeholders, idArgs := inPlaceholders(userIDs)
	args := append([]any{}, idArgs...)
	args = append(args, viewerID)
	clauses := []string{
		"user_id IN (" + placeholders + ")",
		"(user_id = ? OR visibility <> 'private')",
	}
	if before != nil {
		clauses = append(clauses, "(occurred_at < ? OR (occurred_at = ? AND id < ?))")
		args = append(args, before.OccurredAt.UTC(), before.OccurredAt.UTC(), before.ID)
	}
	q := `
		SELECT ` + postColumns + `
		FROM timeline_post
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY occurred_at DESC, id DESC
		LIMIT ?`
	// Fetch one extra row to detect whether another page exists.
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var out []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		return out, &Cursor{OccurredAt: last.OccurredAt, ID: last.ID}, nil
	}
	return out, nil, nil
}

// GetPost returns one post by id, or ErrNotFound.
func (r *SQLiteRepository) GetPost(ctx context.Context, id string) (Post, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+postColumns+`
		FROM timeline_post
		WHERE id = ?
	`, id)
	p, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Post{}, ErrNotFound
	}
	if err != nil {
		return Post{}, err
	}
	return *p, nil
}

// postExists returns nil when a timeline_post row with id exists, or
// ErrNotFound when it does not. AddComment/AddReaction call it so a write
// against a missing post is the contract's ErrNotFound rather than a raw
// "FOREIGN KEY constraint failed" driver error.
func (r *SQLiteRepository) postExists(ctx context.Context, id string) error {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM timeline_post WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// AddComment validates the body and inserts a flat comment on postID.
func (r *SQLiteRepository) AddComment(ctx context.Context, postID, userID, body string) (Comment, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || len(trimmed) > maxCommentBodyLen {
		return Comment{}, ErrValidation
	}
	if err := r.postExists(ctx, postID); err != nil {
		return Comment{}, err
	}
	now := r.now().UTC()
	c := Comment{
		ID:        id.New(),
		PostID:    postID,
		UserID:    userID,
		Body:      trimmed,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO timeline_comment (id, post_id, user_id, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, c.ID, c.PostID, c.UserID, c.Body, c.CreatedAt, c.UpdatedAt); err != nil {
		return Comment{}, err
	}
	return c, nil
}

// DeleteComment soft-deletes the comment, returning ErrNotFound when no live
// comment matches.
func (r *SQLiteRepository) DeleteComment(ctx context.Context, commentID string) error {
	now := r.now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE timeline_comment
		SET deleted_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, now, commentID)
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

// ListComments returns a post's live comments oldest-first.
func (r *SQLiteRepository) ListComments(ctx context.Context, postID string) ([]Comment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+commentColumns+`
		FROM timeline_comment
		WHERE post_id = ? AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC
	`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// AddReaction idempotently adds a reaction of type t by userID on postID. On
// the UNIQUE(post_id, user_id, type) conflict it returns the existing row.
func (r *SQLiteRepository) AddReaction(ctx context.Context, postID, userID string, t ReactionType) (Reaction, error) {
	if !t.Valid() {
		return Reaction{}, ErrValidation
	}
	if err := r.postExists(ctx, postID); err != nil {
		return Reaction{}, err
	}
	now := r.now().UTC()
	newID := id.New()
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO timeline_reaction (id, post_id, user_id, type, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(post_id, user_id, type) DO NOTHING
	`, newID, postID, userID, string(t), now); err != nil {
		return Reaction{}, err
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+reactionColumns+`
		FROM timeline_reaction
		WHERE post_id = ? AND user_id = ? AND type = ?
	`, postID, userID, string(t))
	re, err := scanReaction(row)
	if err != nil {
		return Reaction{}, err
	}
	return *re, nil
}

// RemoveReaction removes the viewer's reaction of type t from postID. It is
// idempotent: removing an absent reaction is not an error.
func (r *SQLiteRepository) RemoveReaction(ctx context.Context, postID, userID string, t ReactionType) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM timeline_reaction
		WHERE post_id = ? AND user_id = ? AND type = ?
	`, postID, userID, string(t))
	return err
}

// ReactionSummaries batch-loads the per-post reaction aggregate for a feed
// page (counts per type, plus the viewer's own types in Mine). It runs at
// most two queries regardless of page size to avoid an N+1. Posts with no
// reactions are absent from the returned map.
func (r *SQLiteRepository) ReactionSummaries(ctx context.Context, postIDs []string, viewerID string) (map[string]ReactionSummary, error) {
	out := make(map[string]ReactionSummary)
	if len(postIDs) == 0 {
		return out, nil
	}
	placeholders, args := inPlaceholders(postIDs)

	// Query 1: counts per (post_id, type) across the page.
	countRows, err := r.db.QueryContext(ctx, `
		SELECT post_id, type, COUNT(*)
		FROM timeline_reaction
		WHERE post_id IN (`+placeholders+`)
		GROUP BY post_id, type
	`, args...)
	if err != nil {
		return nil, err
	}
	defer countRows.Close()
	for countRows.Next() {
		var postID, typ string
		var count int
		if err = countRows.Scan(&postID, &typ, &count); err != nil {
			return nil, err
		}
		s, ok := out[postID]
		if !ok {
			s = ReactionSummary{Counts: make(map[ReactionType]int)}
		}
		s.Counts[ReactionType(typ)] = count
		out[postID] = s
	}
	if err = countRows.Err(); err != nil {
		return nil, err
	}

	// Query 2: the viewer's own reaction types across the page.
	mineArgs := append(append([]any{}, args...), viewerID)
	mineRows, err := r.db.QueryContext(ctx, `
		SELECT post_id, type
		FROM timeline_reaction
		WHERE post_id IN (`+placeholders+`) AND user_id = ?
		ORDER BY post_id, type
	`, mineArgs...)
	if err != nil {
		return nil, err
	}
	defer mineRows.Close()
	for mineRows.Next() {
		var postID, typ string
		if err := mineRows.Scan(&postID, &typ); err != nil {
			return nil, err
		}
		s, ok := out[postID]
		if !ok {
			s = ReactionSummary{Counts: make(map[ReactionType]int)}
		}
		s.Mine = append(s.Mine, ReactionType(typ))
		out[postID] = s
	}
	if err := mineRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CommentCounts batch-loads the live comment count per post for a feed page.
// Posts with no live comments are absent from the returned map.
func (r *SQLiteRepository) CommentCounts(ctx context.Context, postIDs []string) (map[string]int, error) {
	out := make(map[string]int)
	if len(postIDs) == 0 {
		return out, nil
	}
	placeholders, args := inPlaceholders(postIDs)
	rows, err := r.db.QueryContext(ctx, `
		SELECT post_id, COUNT(*)
		FROM timeline_comment
		WHERE post_id IN (`+placeholders+`) AND deleted_at IS NULL
		GROUP BY post_id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var postID string
		var count int
		if err := rows.Scan(&postID, &count); err != nil {
			return nil, err
		}
		out[postID] = count
	}
	return out, rows.Err()
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

// scanPost reads one timeline_post row out of a Row or Rows.
func scanPost(s interface{ Scan(...any) error }) (*Post, error) {
	var (
		p          Post
		sourceType string
		visibility string
	)
	if err := s.Scan(
		&p.ID, &p.UserID, &sourceType, &p.SourceID, &p.OccurredAt, &visibility,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	p.SourceType = SourceType(sourceType)
	p.Visibility = Visibility(visibility)
	return &p, nil
}

// scanComment reads one timeline_comment row out of a Row or Rows.
func scanComment(s interface{ Scan(...any) error }) (*Comment, error) {
	var (
		c         Comment
		deletedAt sql.NullTime
	)
	if err := s.Scan(
		&c.ID, &c.PostID, &c.UserID, &c.Body, &c.CreatedAt, &c.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		c.DeletedAt = &t
	}
	return &c, nil
}

// scanReaction reads one timeline_reaction row out of a Row or Rows.
func scanReaction(s interface{ Scan(...any) error }) (*Reaction, error) {
	var (
		re  Reaction
		typ string
	)
	if err := s.Scan(&re.ID, &re.PostID, &re.UserID, &typ, &re.CreatedAt); err != nil {
		return nil, err
	}
	re.Type = ReactionType(typ)
	return &re, nil
}
