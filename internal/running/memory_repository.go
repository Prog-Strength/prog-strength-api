package running

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation. State lives
// in a map protected by a single RW mutex, mirroring the other domains.
// It is constructed with an Archiver so the Create write ordering matches
// the SQLite implementation.
type MemoryRepository struct {
	mu       sync.RWMutex
	sessions map[string]*Session // id → session (with trackpoints)
	archiver Archiver
	nowFunc  func() time.Time
}

func NewMemoryRepository(archiver Archiver) *MemoryRepository {
	return &MemoryRepository{
		sessions: make(map[string]*Session),
		archiver: archiver,
		nowFunc:  time.Now,
	}
}

func (r *MemoryRepository) Create(ctx context.Context, s *Session, tcx []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Emulate UNIQUE(user_id, garmin_activity_id) over live rows.
	for _, existing := range r.sessions {
		if existing.DeletedAt == nil &&
			existing.UserID == s.UserID &&
			existing.GarminActivityID == s.GarminActivityID {
			return ErrDuplicate
		}
	}

	now := r.nowFunc().UTC()
	s.ID = id.New()
	s.TCXS3Key = fmt.Sprintf("runs/%s/%s.tcx", s.UserID, s.ID)
	s.CreatedAt = now
	s.DeletedAt = nil

	// Archive before storing so a storage failure leaves no row behind,
	// matching the SQLite transaction ordering.
	if err := r.archiver.Put(ctx, s.TCXS3Key, tcx); err != nil {
		return fmt.Errorf("%w: %v", ErrStorage, err)
	}

	stored := *s
	stored.Trackpoints = append([]Trackpoint(nil), s.Trackpoints...)
	r.sessions[s.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetByGarminActivityID(ctx context.Context, userID, garminActivityID string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, s := range r.sessions {
		if s.UserID == userID && s.GarminActivityID == garminActivityID && s.DeletedAt == nil {
			cp := *s
			cp.Trackpoints = nil
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (r *MemoryRepository) List(ctx context.Context, userID string, limit int, before *time.Time) ([]Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Session
	for _, s := range r.sessions {
		if s.UserID != userID || s.DeletedAt != nil {
			continue
		}
		if before != nil && !s.StartTime.Before(*before) {
			continue
		}
		cp := *s
		cp.Trackpoints = nil // list path never ships points
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartTime.After(out[j].StartTime)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *MemoryRepository) Get(ctx context.Context, userID, id string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.sessions[id]
	if !ok || s.UserID != userID || s.DeletedAt != nil {
		return nil, ErrNotFound
	}
	cp := *s
	cp.Trackpoints = append([]Trackpoint(nil), s.Trackpoints...)
	sort.Slice(cp.Trackpoints, func(i, j int) bool {
		return cp.Trackpoints[i].Sequence < cp.Trackpoints[j].Sequence
	})
	return &cp, nil
}

func (r *MemoryRepository) Rename(ctx context.Context, userID, id, name string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[id]
	if !ok || s.UserID != userID || s.DeletedAt != nil {
		return nil, ErrNotFound
	}
	n := name
	s.Name = &n
	cp := *s
	cp.Trackpoints = nil
	return &cp, nil
}

func (r *MemoryRepository) SoftDelete(ctx context.Context, userID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[id]
	if !ok || s.UserID != userID || s.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	s.DeletedAt = &now
	return nil
}

func (r *MemoryRepository) Metrics(ctx context.Context, userID string, now time.Time, loc *time.Location) (Metrics, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var rows []metricRow
	for _, s := range r.sessions {
		if s.UserID != userID || s.DeletedAt != nil {
			continue
		}
		rows = append(rows, metricRow{
			startTime:       s.StartTime,
			distanceMeters:  s.DistanceMeters,
			durationSeconds: s.DurationSeconds,
		})
	}
	return computeMetrics(rows, now, loc), nil
}
