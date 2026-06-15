package follow

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation of the follow
// graph. State lives in a single map keyed by row id, guarded by an RW mutex,
// mirroring the other domains. Its semantics are identical to
// SQLiteRepository: same state-machine guards, ordering (created_at DESC, id
// DESC), keyset pagination with +1-row cursor detection, and defensive copies
// on return — the handler tests run against it.
type MemoryRepository struct {
	mu      sync.RWMutex
	follows map[string]*Follow // id → follow
	now     func() time.Time
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		follows: make(map[string]*Follow),
		now:     time.Now,
	}
}

// findPair returns the row for the ordered (follower, followee) pair, or nil.
// Callers hold the appropriate lock.
func (r *MemoryRepository) findPair(followerID, followeeID string) *Follow {
	for _, f := range r.follows {
		if f.FollowerID == followerID && f.FolloweeID == followeeID {
			return f
		}
	}
	return nil
}

// Request inserts a pending edge follower → followee. Self-follow is rejected
// before touching storage; a pre-existing row for the ordered pair surfaces as
// ErrAlreadyExists; the pending cap is checked first so a spammer is stopped
// before the insert. Followee existence is the handler's concern.
func (r *MemoryRepository) Request(ctx context.Context, followerID, followeeID string) (Follow, error) {
	if followerID == followeeID {
		return Follow{}, ErrSelfFollow
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pending := 0
	for _, f := range r.follows {
		if f.FollowerID == followerID && f.Status == StatusPending {
			pending++
		}
	}
	if pending >= PendingCap {
		return Follow{}, ErrPendingCapExceeded
	}

	// Emulate UNIQUE(follower_id, followee_id): any existing row for the pair
	// (pending or accepted) is a conflict.
	if r.findPair(followerID, followeeID) != nil {
		return Follow{}, ErrAlreadyExists
	}

	now := r.now().UTC()
	f := &Follow{
		ID:         id.New(),
		FollowerID: followerID,
		FolloweeID: followeeID,
		Status:     StatusPending,
		CreatedAt:  now,
	}
	r.follows[f.ID] = f
	return copyFollow(f), nil
}

// Accept flips the pending row addressed to followeeID to accepted.
func (r *MemoryRepository) Accept(ctx context.Context, followeeID, followerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	f := r.findPair(followerID, followeeID)
	if f == nil || f.Status != StatusPending {
		return ErrNotFound
	}
	now := r.now().UTC()
	f.Status = StatusAccepted
	f.AcceptedAt = &now
	return nil
}

// Reject deletes the pending row addressed to followeeID.
func (r *MemoryRepository) Reject(ctx context.Context, followeeID, followerID string) error {
	return r.deleteOne(followerID, followeeID, StatusPending)
}

// Cancel deletes the requester's own pending row.
func (r *MemoryRepository) Cancel(ctx context.Context, followerID, followeeID string) error {
	return r.deleteOne(followerID, followeeID, StatusPending)
}

// Unfollow deletes the requester's own accepted row.
func (r *MemoryRepository) Unfollow(ctx context.Context, followerID, followeeID string) error {
	return r.deleteOne(followerID, followeeID, StatusAccepted)
}

// RemoveFollower deletes the accepted row where the actor is the followee.
func (r *MemoryRepository) RemoveFollower(ctx context.Context, followeeID, followerID string) error {
	return r.deleteOne(followerID, followeeID, StatusAccepted)
}

// deleteOne removes the row for the ordered pair in the given status, returning
// ErrNotFound when nothing matched — the (follower, followee, status) triple is
// the full authorization predicate, matching the SQLite backend.
func (r *MemoryRepository) deleteOne(followerID, followeeID string, status Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for k, f := range r.follows {
		if f.FollowerID == followerID && f.FolloweeID == followeeID && f.Status == status {
			delete(r.follows, k)
			return nil
		}
	}
	return ErrNotFound
}

// Get returns the edge for the ordered pair regardless of status.
func (r *MemoryRepository) Get(ctx context.Context, followerID, followeeID string) (Follow, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	f := r.findPair(followerID, followeeID)
	if f == nil {
		return Follow{}, ErrNotFound
	}
	return copyFollow(f), nil
}

func (r *MemoryRepository) CountPending(ctx context.Context, followerID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := 0
	for _, f := range r.follows {
		if f.FollowerID == followerID && f.Status == StatusPending {
			n++
		}
	}
	return n, nil
}

func (r *MemoryRepository) CountFollowers(ctx context.Context, userID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := 0
	for _, f := range r.follows {
		if f.FolloweeID == userID && f.Status == StatusAccepted {
			n++
		}
	}
	return n, nil
}

func (r *MemoryRepository) CountFollowing(ctx context.Context, userID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := 0
	for _, f := range r.follows {
		if f.FollowerID == userID && f.Status == StatusAccepted {
			n++
		}
	}
	return n, nil
}

// AcceptedFollowees returns the followee ids of viewerID's accepted edges,
// newest-first (created_at DESC, id DESC) to match the SQLite ordering.
func (r *MemoryRepository) AcceptedFollowees(ctx context.Context, viewerID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rows := make([]*Follow, 0)
	for _, f := range r.follows {
		if f.FollowerID == viewerID && f.Status == StatusAccepted {
			rows = append(rows, f)
		}
	}
	sortFollowsNewestFirst(rows)

	out := make([]string, 0, len(rows))
	for _, f := range rows {
		out = append(out, f.FolloweeID)
	}
	return out, nil
}

// Relationship returns viewerID's relationship to otherID.
func (r *MemoryRepository) Relationship(ctx context.Context, viewerID, otherID string) (Relationship, error) {
	if viewerID == otherID {
		return RelationshipSelf, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	rel := RelationshipNone
	for _, f := range r.follows {
		if (f.FollowerID == viewerID && f.FolloweeID == otherID) ||
			(f.FollowerID == otherID && f.FolloweeID == viewerID) {
			rel = mergeRelationship(rel, viewerID, f.FollowerID, f.Status)
		}
	}
	return rel, nil
}

// Relationships batch-computes viewerID's relationship to each id in otherIDs.
func (r *MemoryRepository) Relationships(ctx context.Context, viewerID string, otherIDs []string) (map[string]Relationship, error) {
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

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, f := range r.follows {
		var other string
		switch {
		case f.FollowerID == viewerID:
			other = f.FolloweeID
		case f.FolloweeID == viewerID:
			other = f.FollowerID
		default:
			continue
		}
		if _, ok := out[other]; !ok {
			continue // edge to an id we weren't asked about; ignore
		}
		out[other] = mergeRelationship(out[other], viewerID, f.FollowerID, f.Status)
	}
	return out, nil
}

func (r *MemoryRepository) ListFollowers(ctx context.Context, followeeID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(func(f *Follow) bool {
		return f.FolloweeID == followeeID && f.Status == StatusAccepted
	}, limit, before)
}

func (r *MemoryRepository) ListFollowing(ctx context.Context, followerID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(func(f *Follow) bool {
		return f.FollowerID == followerID && f.Status == StatusAccepted
	}, limit, before)
}

func (r *MemoryRepository) ListIncomingRequests(ctx context.Context, followeeID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(func(f *Follow) bool {
		return f.FolloweeID == followeeID && f.Status == StatusPending
	}, limit, before)
}

func (r *MemoryRepository) ListOutgoingRequests(ctx context.Context, followerID string, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	return r.list(func(f *Follow) bool {
		return f.FollowerID == followerID && f.Status == StatusPending
	}, limit, before)
}

// list collects matching rows newest-first, applies the keyset `before` filter
// and the limit, and returns the next cursor only when a further row exists. It
// fetches limit+1 rows to detect that further page exactly like the SQLite
// backend, so both return identical cursors.
func (r *MemoryRepository) list(match func(*Follow) bool, limit int, before *Cursor) ([]Follow, *Cursor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rows := make([]*Follow, 0)
	for _, f := range r.follows {
		if !match(f) {
			continue
		}
		if before != nil && !cursorBefore(f, before) {
			continue
		}
		rows = append(rows, f)
	}
	sortFollowsNewestFirst(rows)

	// +1-row detection: take limit+1, and if more than limit matched, the
	// last in-page row is the next cursor.
	end := len(rows)
	if end > limit+1 {
		end = limit + 1
	}
	page := rows[:end]

	out := make([]Follow, 0, len(page))
	for _, f := range page {
		out = append(out, copyFollow(f))
	}

	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		return out, &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
	}
	return out, nil, nil
}

// cursorBefore reports whether f is strictly before the keyset cursor in
// (created_at DESC, id DESC) order, matching the SQLite WHERE clause
// `created_at < c.CreatedAt OR (created_at = c.CreatedAt AND id < c.ID)`.
func cursorBefore(f *Follow, c *Cursor) bool {
	cur := c.CreatedAt.UTC()
	if f.CreatedAt.Before(cur) {
		return true
	}
	if f.CreatedAt.Equal(cur) && f.ID < c.ID {
		return true
	}
	return false
}

// sortFollowsNewestFirst orders rows by created_at DESC, id DESC in place.
func sortFollowsNewestFirst(rows []*Follow) {
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].ID > rows[j].ID
	})
}

// copyFollow returns a defensive copy of f, deep-copying the AcceptedAt pointer.
func copyFollow(f *Follow) Follow {
	cp := *f
	if f.AcceptedAt != nil {
		t := *f.AcceptedAt
		cp.AcceptedAt = &t
	}
	return cp
}
