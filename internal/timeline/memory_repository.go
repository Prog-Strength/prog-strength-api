package timeline

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation. State lives in
// maps protected by a single RW mutex, mirroring the other domains. Its
// semantics are identical to SQLiteRepository: same ordering, idempotency,
// validation, soft-delete exclusion, and defensive copies on return — the
// handler tests run against it.
type MemoryRepository struct {
	mu        sync.RWMutex
	posts     map[string]*Post     // id → post
	comments  map[string]*Comment  // id → comment
	reactions map[string]*Reaction // id → reaction
	now       func() time.Time
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		posts:     make(map[string]*Post),
		comments:  make(map[string]*Comment),
		reactions: make(map[string]*Reaction),
		now:       time.Now,
	}
}

func (r *MemoryRepository) EnsurePost(ctx context.Context, ref PostRef) (Post, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Emulate UNIQUE(user_id, source_type, source_id): a conflict returns the
	// existing row unchanged (including its original id/occurred_at).
	for _, p := range r.posts {
		if p.UserID == ref.UserID && p.SourceType == ref.SourceType && p.SourceID == ref.SourceID {
			return *p, nil
		}
	}

	now := r.now().UTC()
	p := &Post{
		ID:         id.New(),
		UserID:     ref.UserID,
		SourceType: ref.SourceType,
		SourceID:   ref.SourceID,
		OccurredAt: ref.OccurredAt.UTC(),
		Visibility: VisibilityFriends,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	r.posts[p.ID] = p
	return *p, nil
}

func (r *MemoryRepository) ListFeed(ctx context.Context, userIDs []string, viewerID string, limit int, before *Cursor) ([]Post, *Cursor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(userIDs) == 0 {
		return nil, nil, nil
	}
	want := make(map[string]bool, len(userIDs))
	for _, uid := range userIDs {
		want[uid] = true
	}

	var out []Post
	for _, p := range r.posts {
		if !want[p.UserID] {
			continue
		}
		// Mirror the SQLite visibility clause: own posts at any visibility,
		// others' posts only when not private.
		if p.UserID != viewerID && p.Visibility == VisibilityPrivate {
			continue
		}
		if before != nil && !cursorBefore(p, before) {
			continue
		}
		out = append(out, *p)
	}
	// Newest-first: occurred_at DESC, id DESC.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].OccurredAt.After(out[j].OccurredAt)
		}
		return out[i].ID > out[j].ID
	})

	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		return out, &Cursor{OccurredAt: last.OccurredAt, ID: last.ID}, nil
	}
	return out, nil, nil
}

// cursorBefore reports whether p is strictly before the keyset cursor in
// (occurred_at DESC, id DESC) order, matching the SQLite WHERE clause
// `occurred_at < c.OccurredAt OR (occurred_at = c.OccurredAt AND id < c.ID)`.
func cursorBefore(p *Post, c *Cursor) bool {
	cur := c.OccurredAt.UTC()
	if p.OccurredAt.Before(cur) {
		return true
	}
	if p.OccurredAt.Equal(cur) && p.ID < c.ID {
		return true
	}
	return false
}

func (r *MemoryRepository) GetPost(ctx context.Context, id string) (Post, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.posts[id]
	if !ok {
		return Post{}, ErrNotFound
	}
	return *p, nil
}

func (r *MemoryRepository) AddComment(ctx context.Context, postID, userID, body string) (Comment, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" || len(trimmed) > maxCommentBodyLen {
		return Comment{}, ErrValidation
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Mirror the FK to timeline_post: a comment on a missing post is ErrNotFound.
	if _, ok := r.posts[postID]; !ok {
		return Comment{}, ErrNotFound
	}

	now := r.now().UTC()
	c := &Comment{
		ID:        id.New(),
		PostID:    postID,
		UserID:    userID,
		Body:      trimmed,
		CreatedAt: now,
		UpdatedAt: now,
	}
	r.comments[c.ID] = c
	return *c, nil
}

func (r *MemoryRepository) DeleteComment(ctx context.Context, commentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.comments[commentID]
	if !ok || c.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.now().UTC()
	c.DeletedAt = &now
	return nil
}

func (r *MemoryRepository) ListComments(ctx context.Context, postID string) ([]Comment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Comment
	for _, c := range r.comments {
		if c.PostID != postID || c.DeletedAt != nil {
			continue
		}
		out = append(out, copyComment(c))
	}
	// Oldest-first: created_at ASC, id ASC.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (r *MemoryRepository) AddReaction(ctx context.Context, postID, userID string, t ReactionType) (Reaction, error) {
	if !t.Valid() {
		return Reaction{}, ErrValidation
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Mirror the FK to timeline_post: a reaction on a missing post is ErrNotFound.
	if _, ok := r.posts[postID]; !ok {
		return Reaction{}, ErrNotFound
	}

	// Emulate UNIQUE(post_id, user_id, type): a conflict returns the existing.
	for _, re := range r.reactions {
		if re.PostID == postID && re.UserID == userID && re.Type == t {
			return *re, nil
		}
	}
	now := r.now().UTC()
	re := &Reaction{
		ID:        id.New(),
		PostID:    postID,
		UserID:    userID,
		Type:      t,
		CreatedAt: now,
	}
	r.reactions[re.ID] = re
	return *re, nil
}

func (r *MemoryRepository) RemoveReaction(ctx context.Context, postID, userID string, t ReactionType) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, re := range r.reactions {
		if re.PostID == postID && re.UserID == userID && re.Type == t {
			delete(r.reactions, id)
			return nil // idempotent: nothing else to do
		}
	}
	return nil
}

func (r *MemoryRepository) ReactionSummaries(ctx context.Context, postIDs []string, viewerID string) (map[string]ReactionSummary, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	want := make(map[string]bool, len(postIDs))
	for _, p := range postIDs {
		want[p] = true
	}

	out := make(map[string]ReactionSummary)
	// Collect the viewer's own types per post so Mine can be sorted to a
	// stable order, matching the SQLite query's ORDER BY post_id, type.
	mine := make(map[string][]ReactionType)
	for _, re := range r.reactions {
		if !want[re.PostID] {
			continue
		}
		s, ok := out[re.PostID]
		if !ok {
			s = ReactionSummary{Counts: make(map[ReactionType]int)}
		}
		s.Counts[re.Type]++
		out[re.PostID] = s
		if re.UserID == viewerID {
			mine[re.PostID] = append(mine[re.PostID], re.Type)
		}
	}
	for postID, types := range mine {
		sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
		s := out[postID]
		s.Mine = types
		out[postID] = s
	}
	return out, nil
}

func (r *MemoryRepository) CommentCounts(ctx context.Context, postIDs []string) (map[string]int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	want := make(map[string]bool, len(postIDs))
	for _, p := range postIDs {
		want[p] = true
	}

	out := make(map[string]int)
	for _, c := range r.comments {
		if !want[c.PostID] || c.DeletedAt != nil {
			continue
		}
		out[c.PostID]++
	}
	return out, nil
}

// deletePost emulates the ON DELETE CASCADE foreign key: removing a post row
// also removes its comments and reactions. It is unexported because the
// Repository interface intentionally has no DeletePost (post lifecycle is the
// source domain's concern); it exists so the cascade can be exercised in
// tests without a raw SQL backdoor.
func (r *MemoryRepository) deletePost(postID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.posts, postID)
	for id, c := range r.comments {
		if c.PostID == postID {
			delete(r.comments, id)
		}
	}
	for id, re := range r.reactions {
		if re.PostID == postID {
			delete(r.reactions, id)
		}
	}
}

func copyComment(c *Comment) Comment {
	cp := *c
	if c.DeletedAt != nil {
		t := *c.DeletedAt
		cp.DeletedAt = &t
	}
	return cp
}
