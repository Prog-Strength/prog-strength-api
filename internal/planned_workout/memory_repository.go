package plannedworkout

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is the dev/test in-memory implementation. State lives in
// a single map keyed by id, protected by a RW mutex — same pattern as the
// other domain packages. Stored and returned values are deep-copied so
// callers can never mutate internal state through the nested slices/pointers.
type MemoryRepository struct {
	mu      sync.RWMutex
	plans   map[string]*PlannedWorkout // id → plan
	nowFunc func() time.Time           // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		plans:   make(map[string]*PlannedWorkout),
		nowFunc: time.Now,
	}
}

func (r *MemoryRepository) Create(ctx context.Context, pw *PlannedWorkout) error {
	if pw.Status == "" {
		pw.Status = StatusPlanned
	}
	if err := pw.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc().UTC()
	pw.ID = id.New()
	pw.CreatedAt = now
	pw.UpdatedAt = now
	pw.DeletedAt = nil
	assignAgendaIDs(pw)

	r.plans[pw.ID] = clonePlan(pw)
	return nil
}

func (r *MemoryRepository) Get(ctx context.Context, userID, planID string) (*PlannedWorkout, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	pw, ok := r.plans[planID]
	if !ok || pw.UserID != userID || pw.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return clonePlan(pw), nil
}

func (r *MemoryRepository) List(ctx context.Context, userID string, since, until *time.Time) ([]PlannedWorkout, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []PlannedWorkout
	for _, pw := range r.plans {
		if pw.UserID != userID || pw.DeletedAt != nil {
			continue
		}
		if since != nil && pw.ScheduledStartUTC.Before(*since) {
			continue
		}
		if until != nil && !pw.ScheduledStartUTC.Before(*until) {
			continue
		}
		out = append(out, *clonePlan(pw))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ScheduledStartUTC.Before(out[j].ScheduledStartUTC)
	})
	return out, nil
}

func (r *MemoryRepository) Update(ctx context.Context, pw *PlannedWorkout) error {
	if pw.Status == "" {
		pw.Status = StatusPlanned
	}
	if err := pw.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.plans[pw.ID]
	if !ok || existing.UserID != pw.UserID || existing.DeletedAt != nil {
		return ErrNotFound
	}

	now := r.nowFunc().UTC()
	updated := clonePlan(pw)
	// Preserve immutable provenance; bump updated_at; replace the agenda.
	updated.ID = existing.ID
	updated.UserID = existing.UserID
	updated.CreatedAt = existing.CreatedAt
	updated.DeletedAt = nil
	updated.UpdatedAt = now
	assignAgendaIDs(updated)

	r.plans[updated.ID] = updated
	return nil
}

func (r *MemoryRepository) Delete(ctx context.Context, userID, planID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.plans[planID]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.nowFunc().UTC()
	existing.DeletedAt = &now
	return nil
}

func (r *MemoryRepository) SetStatus(ctx context.Context, userID, planID string, status Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.plans[planID]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	existing.Status = status
	existing.UpdatedAt = r.nowFunc().UTC()
	return nil
}

func (r *MemoryRepository) SetCompletion(ctx context.Context, userID, planID, sessionID string, kind SessionKind) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.plans[planID]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	existing.Status = StatusCompleted
	sid, k := sessionID, kind
	existing.CompletedSessionID = &sid
	existing.CompletedSessionKind = &k
	existing.UpdatedAt = r.nowFunc().UTC()
	return nil
}

func (r *MemoryRepository) SetGoogleSync(ctx context.Context, userID, planID string, eventID *string, status GoogleSyncStatus, lastErr *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.plans[planID]
	if !ok || existing.UserID != userID || existing.DeletedAt != nil {
		return ErrNotFound
	}
	existing.GoogleEventID = clonePtrStr(eventID)
	s := status
	existing.GoogleSyncStatus = &s
	existing.LastSyncError = clonePtrStr(lastErr)
	existing.UpdatedAt = r.nowFunc().UTC()
	return nil
}

// assignAgendaIDs stamps a fresh id on every exercise/set and sets
// order_index to the slice position so callers don't have to manage either.
func assignAgendaIDs(pw *PlannedWorkout) {
	for i := range pw.Exercises {
		pw.Exercises[i].ID = id.New()
		pw.Exercises[i].OrderIndex = i
		for j := range pw.Exercises[i].Sets {
			pw.Exercises[i].Sets[j].ID = id.New()
			pw.Exercises[i].Sets[j].OrderIndex = j
		}
	}
}

// clonePlan deep-copies a plan, including the nested Exercises/Sets slices
// and every pointer field, so neither the stored value nor a returned value
// shares mutable memory with the caller.
func clonePlan(src *PlannedWorkout) *PlannedWorkout {
	dst := *src
	dst.Name = clonePtrStr(src.Name)
	dst.Notes = clonePtrStr(src.Notes)
	dst.CompletedSessionID = clonePtrStr(src.CompletedSessionID)
	dst.LastSyncError = clonePtrStr(src.LastSyncError)
	dst.GoogleEventID = clonePtrStr(src.GoogleEventID)
	if src.CompletedSessionKind != nil {
		k := *src.CompletedSessionKind
		dst.CompletedSessionKind = &k
	}
	if src.CalendarDetail != nil {
		d := *src.CalendarDetail
		dst.CalendarDetail = &d
	}
	if src.GoogleSyncStatus != nil {
		s := *src.GoogleSyncStatus
		dst.GoogleSyncStatus = &s
	}
	if src.DeletedAt != nil {
		t := *src.DeletedAt
		dst.DeletedAt = &t
	}
	if src.Exercises != nil {
		dst.Exercises = make([]PlannedExercise, len(src.Exercises))
		for i, ex := range src.Exercises {
			ce := ex
			ce.Notes = clonePtrStr(ex.Notes)
			if ex.Sets != nil {
				ce.Sets = make([]PlannedSet, len(ex.Sets))
				for j, s := range ex.Sets {
					cs := s
					cs.TargetReps = clonePtrInt(s.TargetReps)
					cs.TargetWeight = clonePtrF(s.TargetWeight)
					cs.Unit = clonePtrStr(s.Unit)
					cs.TargetRPE = clonePtrF(s.TargetRPE)
					ce.Sets[j] = cs
				}
			}
			dst.Exercises[i] = ce
		}
	}
	return &dst
}

func clonePtrStr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func clonePtrInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func clonePtrF(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
