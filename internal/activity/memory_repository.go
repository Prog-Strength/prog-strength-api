package activity

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
	mu         sync.RWMutex
	activities map[string]*Activity // id → activity (with trackpoints)
	archiver   Archiver
	nowFunc    func() time.Time
}

func NewMemoryRepository(archiver Archiver) *MemoryRepository {
	return &MemoryRepository{
		activities: make(map[string]*Activity),
		archiver:   archiver,
		nowFunc:    time.Now,
	}
}

func (r *MemoryRepository) Create(ctx context.Context, a *Activity, tcx []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Emulate UNIQUE(user_id, ingest_source, source_activity_id) over
	// live rows.
	for _, existing := range r.activities {
		if existing.DeletedAt == nil &&
			existing.UserID == a.UserID &&
			existing.IngestSource == a.IngestSource &&
			existing.SourceActivityID == a.SourceActivityID {
			return ErrDuplicate
		}
	}

	now := r.nowFunc().UTC()
	a.ID = id.New()
	key, err := buildTCXKey(a.UserID, a.ActivityType, a.StartTime, a.ID)
	if err != nil {
		return fmt.Errorf("activity: build s3 key: %w", err)
	}
	a.TCXS3Key = key
	a.CreatedAt = now
	a.DeletedAt = nil

	// Archive before storing so a storage failure leaves no row behind,
	// matching the SQLite transaction ordering.
	if err := r.archiver.Put(ctx, a.TCXS3Key, tcx, ObjectMetadata{IngestSource: a.IngestSource}); err != nil {
		return fmt.Errorf("%w: %w", ErrStorage, err)
	}

	stored := *a
	stored.Trackpoints = append([]Trackpoint(nil), a.Trackpoints...)
	stored.BestEfforts = append([]ActivityBestEffort(nil), a.BestEfforts...)
	r.activities[a.ID] = &stored
	return nil
}

func (r *MemoryRepository) GetBySourceActivityID(ctx context.Context, userID string, source IngestSource, sourceActivityID string) (*Activity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, a := range r.activities {
		if a.UserID == userID && a.IngestSource == source && a.SourceActivityID == sourceActivityID && a.DeletedAt == nil {
			cp := *a
			cp.Trackpoints = nil
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (r *MemoryRepository) List(ctx context.Context, userID string, limit int, before *time.Time) ([]Activity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Activity
	for _, a := range r.activities {
		if a.UserID != userID || a.DeletedAt != nil {
			continue
		}
		if before != nil && !a.StartTime.Before(*before) {
			continue
		}
		cp := *a
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

func (r *MemoryRepository) ListInRange(ctx context.Context, userID string, since, until *time.Time) ([]Activity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Activity
	for _, a := range r.activities {
		if a.UserID != userID || a.DeletedAt != nil {
			continue
		}
		if since != nil && a.StartTime.Before(*since) {
			continue
		}
		// Half-open: an activity at exactly `until` belongs to the NEXT
		// range, so callers can pass adjacent month boundaries without
		// double-counting the midnight activity.
		if until != nil && !a.StartTime.Before(*until) {
			continue
		}
		cp := *a
		cp.Trackpoints = nil
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartTime.After(out[j].StartTime)
	})
	return out, nil
}

func (r *MemoryRepository) Get(ctx context.Context, userID, activityID string) (*Activity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, ok := r.activities[activityID]
	if !ok || a.UserID != userID || a.DeletedAt != nil {
		return nil, ErrNotFound
	}
	cp := *a
	cp.Trackpoints = append([]Trackpoint(nil), a.Trackpoints...)
	sort.Slice(cp.Trackpoints, func(i, j int) bool {
		return cp.Trackpoints[i].Sequence < cp.Trackpoints[j].Sequence
	})
	return &cp, nil
}

func (r *MemoryRepository) Rename(ctx context.Context, userID, activityID, name string) (*Activity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.activities[activityID]
	if !ok || a.UserID != userID || a.DeletedAt != nil {
		return nil, ErrNotFound
	}
	n := name
	a.Name = &n
	cp := *a
	cp.Trackpoints = nil
	return &cp, nil
}

func (r *MemoryRepository) SoftDelete(ctx context.Context, userID, activityID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.activities[activityID]
	if !ok || a.UserID != userID || a.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	a.DeletedAt = &now
	return nil
}

func (r *MemoryRepository) RunningMetrics(ctx context.Context, userID string, now time.Time, loc *time.Location) (Metrics, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var rows []metricRow
	for _, a := range r.activities {
		if a.UserID != userID || a.DeletedAt != nil || a.ActivityType != ActivityRunning {
			continue
		}
		rows = append(rows, metricRow{
			startTime:       a.StartTime,
			distanceMeters:  a.DistanceMeters,
			durationSeconds: a.DurationSeconds,
		})
	}
	return computeMetrics(rows, now, loc), nil
}

// ListRunningSamplesSince mirrors the SQLite projection: the
// (StartTime, DistanceMeters) pairs of the user's live ActivityRunning rows
// starting at/after `since`. Walks/rides are excluded.
func (r *MemoryRepository) ListRunningSamplesSince(ctx context.Context, userID string, since time.Time) ([]RunSample, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []RunSample
	for _, a := range r.activities {
		if a.UserID != userID || a.DeletedAt != nil || a.ActivityType != ActivityRunning {
			continue
		}
		if a.StartTime.Before(since) {
			continue
		}
		out = append(out, RunSample{StartTime: a.StartTime, DistanceMeters: a.DistanceMeters})
	}
	return out, nil
}

// GetUserRunningBestEfforts mirrors the SQLite per-distance MIN with the
// earliest-start tie-break. Computes the current best at each distance
// across the user's live running activities.
func (r *MemoryRepository) GetUserRunningBestEfforts(ctx context.Context, userID string) ([]RunningBestEffort, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// best[distanceKey] holds the current winner; a new candidate wins when
	// it's strictly faster, or equal and earlier-started.
	best := make(map[string]RunningBestEffort)
	for _, a := range r.activities {
		if a.UserID != userID || a.DeletedAt != nil || a.ActivityType != ActivityRunning {
			continue
		}
		for _, e := range a.BestEfforts {
			cand := RunningBestEffort{
				DistanceKey:       e.DistanceKey,
				DurationSeconds:   e.DurationSeconds,
				ActivityID:        a.ID,
				ActivityStartTime: a.StartTime,
			}
			cur, ok := best[e.DistanceKey]
			if !ok ||
				cand.DurationSeconds < cur.DurationSeconds ||
				(cand.DurationSeconds == cur.DurationSeconds && cand.ActivityStartTime.Before(cur.ActivityStartTime)) {
				best[e.DistanceKey] = cand
			}
		}
	}

	// Emit in StandardDistances order so the result is stable, matching the
	// handler's expected ordering.
	var out []RunningBestEffort
	for _, d := range StandardDistances {
		if b, ok := best[d.Key]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}

// GetRunningBestEffortHistory mirrors the SQLite history query: every
// best-effort row at distanceKey for the user's live running activities,
// ascending by start_time.
func (r *MemoryRepository) GetRunningBestEffortHistory(ctx context.Context, userID, distanceKey string) ([]BestEffortPoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []BestEffortPoint
	for _, a := range r.activities {
		if a.UserID != userID || a.DeletedAt != nil || a.ActivityType != ActivityRunning {
			continue
		}
		for _, e := range a.BestEfforts {
			if e.DistanceKey != distanceKey {
				continue
			}
			out = append(out, BestEffortPoint{
				ActivityID:             a.ID,
				ActivityStartTime:      a.StartTime,
				DurationSeconds:        e.DurationSeconds,
				ActivityDistanceMeters: a.DistanceMeters,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ActivityStartTime.Before(out[j].ActivityStartTime)
	})
	return out, nil
}
