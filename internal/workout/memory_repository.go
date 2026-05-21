package workout

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *MemoryRepository satisfies Repository.
var _ Repository = (*MemoryRepository)(nil)

// MemoryRepository is an in-memory implementation of Repository.
// It's safe for concurrent use. Data is lost when the process exits —
// intended for development, testing, and early prototyping.
type MemoryRepository struct {
	mu       sync.RWMutex
	workouts map[string]*Workout
	// history keyed by workout_id so cascade on Update/Delete is O(1).
	// The in-memory repo is dev/test only, so the per-list filter for
	// reads is fine — beats maintaining a second index.
	history map[string][]OneRepMaxEntry
	// personal records keyed by "userID:exerciseID" — at most one PR
	// per (user, exercise), matching the SQLite uniqueness constraint.
	personalRecords map[string]*PersonalRecord
	// PR events stored flat; filtered on each operation. Dev/test only,
	// so the cost is acceptable.
	personalRecordEvents []PersonalRecordEvent
	// User-curated headline-exercise selection, keyed by userID. The
	// stored slice is already in display order — ReplaceUserHeadlineExercises
	// writes positions starting at zero so reads can just hand the slice
	// back. Absent key = "user hasn't customized," callers fall back to
	// HeadlineExercises.
	userHeadlineExercises map[string][]UserHeadlineExercise
	now                   func() time.Time // injectable for tests
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		workouts:              make(map[string]*Workout),
		history:               make(map[string][]OneRepMaxEntry),
		personalRecords:       make(map[string]*PersonalRecord),
		userHeadlineExercises: make(map[string][]UserHeadlineExercise),
		now:                   time.Now,
	}
}

func (r *MemoryRepository) Create(ctx context.Context, w *Workout) error {
	if err := w.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now().UTC()
	w.ID = id.New()
	w.CreatedAt = now
	w.UpdatedAt = now

	// Store a copy so external mutation doesn't affect our state.
	stored := *w
	r.workouts[w.ID] = &stored
	r.writeHistoryLocked(*w, now)
	for _, exerciseID := range ExercisesInWorkout(*w) {
		r.recomputePersonalRecordLocked(w.UserID, exerciseID, now)
	}
	return nil
}

func (r *MemoryRepository) GetByID(ctx context.Context, id string) (*Workout, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	w, ok := r.workouts[id]
	if !ok || w.DeletedAt != nil {
		return nil, ErrNotFound
	}
	out := *w
	return &out, nil
}

func (r *MemoryRepository) ListByUser(ctx context.Context, userID string, opts ListOptions) ([]Workout, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []Workout
	for _, w := range r.workouts {
		if w.UserID != userID || w.DeletedAt != nil {
			continue
		}
		if opts.Since != nil && w.PerformedAt.Before(*opts.Since) {
			continue
		}
		if opts.Until != nil && w.PerformedAt.After(*opts.Until) {
			continue
		}
		results = append(results, *w)
	}

	// Most recent first.
	sort.Slice(results, func(i, j int) bool {
		return results[i].PerformedAt.After(results[j].PerformedAt)
	})

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if opts.Offset >= len(results) {
		return []Workout{}, nil
	}
	end := opts.Offset + limit
	if end > len(results) {
		end = len(results)
	}
	return results[opts.Offset:end], nil
}

func (r *MemoryRepository) CountByUser(
	ctx context.Context,
	userID string,
	opts ListOptions,
) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, w := range r.workouts {
		if w.UserID != userID || w.DeletedAt != nil {
			continue
		}
		if opts.Since != nil && w.PerformedAt.Before(*opts.Since) {
			continue
		}
		if opts.Until != nil && w.PerformedAt.After(*opts.Until) {
			continue
		}
		count++
	}
	return count, nil
}

func (r *MemoryRepository) Update(ctx context.Context, w *Workout) error {
	if err := w.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.workouts[w.ID]
	if !ok || existing.DeletedAt != nil {
		return ErrNotFound
	}

	// Union of new-exercise set and previously-affected exercise set,
	// matching the SQLite update path. Compute affected set BEFORE
	// overwriting the stored workout so we can read the prior PR/event
	// state.
	affected := r.affectedExercisesLocked(w.ID)
	for _, exerciseID := range ExercisesInWorkout(*w) {
		affected[exerciseID] = struct{}{}
	}

	w.CreatedAt = existing.CreatedAt
	w.UpdatedAt = r.now().UTC()
	stored := *w
	r.workouts[w.ID] = &stored
	r.writeHistoryLocked(*w, w.UpdatedAt)

	for exerciseID := range affected {
		r.recomputePersonalRecordLocked(w.UserID, exerciseID, w.UpdatedAt)
	}
	return nil
}

func (r *MemoryRepository) Delete(ctx context.Context, workoutID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workouts[workoutID]
	if !ok || w.DeletedAt != nil {
		return ErrNotFound
	}
	now := r.now().UTC()

	// Capture affected exercises (and the user) BEFORE flipping the
	// soft-delete flag so the recompute reads consistent state.
	affected := r.affectedExercisesLocked(workoutID)
	for _, e := range w.Exercises {
		affected[e.ExerciseID] = struct{}{}
	}
	userID := w.UserID

	w.DeletedAt = &now
	w.UpdatedAt = now
	delete(r.history, workoutID)

	for exerciseID := range affected {
		r.recomputePersonalRecordLocked(userID, exerciseID, now)
	}
	return nil
}

func (r *MemoryRepository) ListOneRepMaxHistory(
	ctx context.Context,
	userID, exerciseID string,
	since, until *time.Time,
) ([]OneRepMaxEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []OneRepMaxEntry
	for _, entries := range r.history {
		for _, e := range entries {
			if e.UserID != userID || e.ExerciseID != exerciseID {
				continue
			}
			if since != nil && e.PerformedAt.Before(*since) {
				continue
			}
			if until != nil && e.PerformedAt.After(*until) {
				continue
			}
			out = append(out, e)
		}
	}
	// Most recent first, matching the SQLite implementation.
	sort.Slice(out, func(i, j int) bool {
		return out[i].PerformedAt.After(out[j].PerformedAt)
	})
	return out, nil
}

// writeHistoryLocked replaces the history rows for w with a freshly
// aggregated set. Caller must hold r.mu in write mode. Used by Create
// and Update — same aggregation function the SQLite repository uses.
func (r *MemoryRepository) writeHistoryLocked(w Workout, now time.Time) {
	delete(r.history, w.ID)
	entries := AggregateOneRepMax(w)
	if len(entries) == 0 {
		return
	}
	for i := range entries {
		entries[i].ID = id.New()
		entries[i].CreatedAt = now
		entries[i].UpdatedAt = now
	}
	r.history[w.ID] = entries
}

func (r *MemoryRepository) ListPersonalRecords(
	ctx context.Context,
	userID string,
) ([]PersonalRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []PersonalRecord
	for _, pr := range r.personalRecords {
		if pr.UserID != userID {
			continue
		}
		out = append(out, *pr)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AchievedAt.After(out[j].AchievedAt)
	})
	return out, nil
}

func (r *MemoryRepository) ListPersonalRecordEventsByWorkouts(
	ctx context.Context,
	workoutIDs []string,
) ([]PersonalRecordEvent, error) {
	if len(workoutIDs) == 0 {
		return nil, nil
	}
	want := make(map[string]struct{}, len(workoutIDs))
	for _, w := range workoutIDs {
		want[w] = struct{}{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []PersonalRecordEvent
	for _, e := range r.personalRecordEvents {
		if _, ok := want[e.WorkoutID]; ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AchievedAt.After(out[j].AchievedAt)
	})
	return out, nil
}

// affectedExercisesLocked returns exercise IDs touched by any data
// keyed on the given workout — same union as the SQLite repo's
// affectedExercisesForRecomputeTx.
func (r *MemoryRepository) affectedExercisesLocked(workoutID string) map[string]struct{} {
	out := map[string]struct{}{}
	if w, ok := r.workouts[workoutID]; ok {
		for _, e := range w.Exercises {
			out[e.ExerciseID] = struct{}{}
		}
	}
	for key, pr := range r.personalRecords {
		_ = key
		if pr.WorkoutID == workoutID {
			out[pr.ExerciseID] = struct{}{}
		}
	}
	for _, e := range r.personalRecordEvents {
		if e.WorkoutID == workoutID {
			out[e.ExerciseID] = struct{}{}
		}
	}
	return out
}

// recomputePersonalRecordLocked re-derives the PR row and event chain
// for one (user, exercise) pair against the current state of
// r.workouts. Caller must hold r.mu in write mode.
func (r *MemoryRepository) recomputePersonalRecordLocked(
	userID, exerciseID string,
	now time.Time,
) {
	key := userID + ":" + exerciseID

	// Gather snapshots of every non-deleted workout containing this
	// exercise, in chronological order.
	var snaps []WorkoutSnapshot
	for _, w := range r.workouts {
		if w.UserID != userID || w.DeletedAt != nil {
			continue
		}
		var blocks []ExerciseSnapshot
		for _, e := range w.Exercises {
			if e.ExerciseID == exerciseID {
				blocks = append(blocks, ExerciseSnapshot{
					ExerciseID: exerciseID,
					Sets:       append([]Set(nil), e.Sets...),
				})
			}
		}
		if len(blocks) == 0 {
			continue
		}
		snaps = append(snaps, WorkoutSnapshot{
			ID:          w.ID,
			UserID:      w.UserID,
			PerformedAt: w.PerformedAt,
			Exercises:   blocks,
		})
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].PerformedAt.Before(snaps[j].PerformedAt)
	})

	pr, events := RecomputePersonalRecord(snaps, exerciseID)

	// Drop existing state for (user, exercise) before re-inserting.
	delete(r.personalRecords, key)
	filtered := r.personalRecordEvents[:0]
	for _, e := range r.personalRecordEvents {
		if e.UserID == userID && e.ExerciseID == exerciseID {
			continue
		}
		filtered = append(filtered, e)
	}
	r.personalRecordEvents = filtered

	if pr != nil {
		pr.ID = id.New()
		pr.CreatedAt = now
		pr.UpdatedAt = now
		r.personalRecords[key] = pr
	}
	for i := range events {
		events[i].ID = id.New()
		events[i].CreatedAt = now
		r.personalRecordEvents = append(r.personalRecordEvents, events[i])
	}
}

func (r *MemoryRepository) ListUserHeadlineExercises(
	ctx context.Context,
	userID string,
) ([]UserHeadlineExercise, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stored := r.userHeadlineExercises[userID]
	if len(stored) == 0 {
		return nil, nil
	}
	// Defensive copy — same convention as the rest of this repo,
	// callers should not be able to mutate our state.
	out := make([]UserHeadlineExercise, len(stored))
	copy(out, stored)
	return out, nil
}

func (r *MemoryRepository) ReplaceUserHeadlineExercises(
	ctx context.Context,
	userID string,
	exerciseIDs []string,
	now time.Time,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(exerciseIDs) == 0 {
		delete(r.userHeadlineExercises, userID)
		return nil
	}
	rows := make([]UserHeadlineExercise, len(exerciseIDs))
	for i, exerciseID := range exerciseIDs {
		rows[i] = UserHeadlineExercise{
			UserID:     userID,
			ExerciseID: exerciseID,
			Position:   i,
			CreatedAt:  now,
		}
	}
	r.userHeadlineExercises[userID] = rows
	return nil
}
