package workout

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// PersonalRecord is the current heaviest set the user has logged on a
// single exercise — one row of `personal_records`. The single
// load-bearing field for ranking is Weight; Reps is stored alongside
// for display ("305 lb × 3") and so that a future per-rep-range PR
// feature is an additive change.
type PersonalRecord struct {
	ID          string
	UserID      string
	ExerciseID  string
	WorkoutID   string
	Weight      float64
	Reps        int
	Unit        user.WeightUnit
	AchievedAt  time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PersonalRecordEvent is one entry in the append-only log of PR breaks
// — one row of `personal_record_events`. The frontend uses
// `workout_id` to badge sessions that produced a record; the agent
// uses `achieved_at` to find recent breaks to congratulate.
//
// PreviousWeight / PreviousReps / PreviousUnit are nullable: a user's
// very first logged set on an exercise produces an event with no
// previous record to compare against.
type PersonalRecordEvent struct {
	ID             string
	UserID         string
	ExerciseID     string
	WorkoutID      string
	Weight         float64
	Reps           int
	Unit           user.WeightUnit
	PreviousWeight *float64
	PreviousReps   *int
	PreviousUnit   *user.WeightUnit
	AchievedAt     time.Time
	CreatedAt      time.Time
}

// kgPerLb is the canonical conversion factor used when a candidate set
// is in a different unit than the current PR. Most users stay in one
// unit so this only fires for mixed-unit edge cases.
const kgPerLb = 0.45359237

// weightInLb returns a weight expressed in pounds regardless of the
// original unit. Used only for cross-unit comparison; the stored row
// keeps the original unit and value.
func weightInLb(w float64, u user.WeightUnit) float64 {
	if u == user.WeightUnitKilograms {
		return w / kgPerLb
	}
	return w
}

// heaviestSet returns the heaviest set across all of `sets`, with lb
// chosen as the comparison basis. The returned values are in the
// original unit of the winning set — callers store the row in that
// unit, not a converted one. Returns (_, false) when sets is empty.
func heaviestSet(sets []Set) (Set, bool) {
	if len(sets) == 0 {
		return Set{}, false
	}
	best := sets[0]
	bestLb := weightInLb(best.Weight, best.Unit)
	for _, s := range sets[1:] {
		if weightInLb(s.Weight, s.Unit) > bestLb {
			best = s
			bestLb = weightInLb(s.Weight, s.Unit)
		}
	}
	return best, true
}

// WorkoutSnapshot is the minimal projection of a workout that PR
// derivation needs: identity, timestamp, and the per-exercise sets.
// Used by RecomputePersonalRecord so the pure function doesn't depend
// on the full Workout struct's optional fields (notes, ended_at, etc.).
type WorkoutSnapshot struct {
	ID          string
	UserID      string
	PerformedAt time.Time
	Exercises   []ExerciseSnapshot
}

// ExerciseSnapshot is the per-workout per-exercise projection used by
// PR derivation. Multiple snapshots can share the same ExerciseID in
// a single workout (e.g. warmup block + main block) — the recompute
// folds them.
type ExerciseSnapshot struct {
	ExerciseID string
	Sets       []Set
}

// RecomputePersonalRecord walks the given workout snapshots in
// chronological order and produces (a) the final PR state and (b) the
// chain of events emitted along the way for a single (user, exercise).
//
// Both returned values are nil when no qualifying set exists across
// the given workouts. The same function services the live write path
// and the backfill so backfilled rows are guaranteed to match what
// the live path would have produced.
//
// Pure function: no IO, no time.Now, no ID generation. Caller fills
// IDs and timestamps on the returned values before persisting.
//
// Tie semantics: a candidate set whose weight exactly matches the
// running max does *not* overwrite the record and does *not* emit an
// event. The earliest workout that set the weight keeps the claim.
//
// Same-workout ramping: if a single workout contains multiple sets
// that would each set a new PR (e.g. 295 × 1, 300 × 1, 305 × 1), only
// one event is emitted for that workout — capturing the heaviest set.
// `previous` references the PR weight *before* the workout, not the
// intra-workout intermediate. This matches Open Question #2 in the SOW.
func RecomputePersonalRecord(
	snapshots []WorkoutSnapshot,
	exerciseID string,
) (*PersonalRecord, []PersonalRecordEvent) {
	// Sort guarantee: caller passes snapshots already sorted by
	// PerformedAt ASC. We don't re-sort here so the function stays
	// pure and predictable (callers can verify their own ordering).

	var current *PersonalRecord
	var events []PersonalRecordEvent

	for _, snap := range snapshots {
		// Flatten same-exercise blocks within this workout and pick
		// its heaviest set on the target exercise.
		var sets []Set
		for _, e := range snap.Exercises {
			if e.ExerciseID == exerciseID {
				sets = append(sets, e.Sets...)
			}
		}
		// Drop bodyweight sets (weight == 0). The personal_records
		// schema enforces CHECK(weight > 0) — the table tracks
		// heaviest *external* load. Pull-up / push-up progression is
		// measured in reps and belongs in a future per-rep-range PR
		// feature, not here. Weighted variants (e.g. weighted pull-ups
		// at +25 lb) still produce PR rows on their qualifying sets;
		// only the bodyweight portion of mixed-set workouts is dropped.
		filtered := sets[:0]
		for _, s := range sets {
			if s.Weight > 0 {
				filtered = append(filtered, s)
			}
		}
		heaviest, ok := heaviestSet(filtered)
		if !ok {
			continue
		}

		// Compare on lb basis so mixed-unit users get honest ranking;
		// store in the candidate's original unit on a break.
		var currentLb float64
		if current != nil {
			currentLb = weightInLb(current.Weight, current.Unit)
		}
		candidateLb := weightInLb(heaviest.Weight, heaviest.Unit)

		if current == nil || candidateLb > currentLb {
			var prevWeight *float64
			var prevReps *int
			var prevUnit *user.WeightUnit
			if current != nil {
				w, r, u := current.Weight, current.Reps, current.Unit
				prevWeight = &w
				prevReps = &r
				prevUnit = &u
			}
			events = append(events, PersonalRecordEvent{
				UserID:         snap.UserID,
				ExerciseID:     exerciseID,
				WorkoutID:      snap.ID,
				Weight:         heaviest.Weight,
				Reps:           heaviest.Reps,
				Unit:           heaviest.Unit,
				PreviousWeight: prevWeight,
				PreviousReps:   prevReps,
				PreviousUnit:   prevUnit,
				AchievedAt:     snap.PerformedAt,
			})
			current = &PersonalRecord{
				UserID:     snap.UserID,
				ExerciseID: exerciseID,
				WorkoutID:  snap.ID,
				Weight:     heaviest.Weight,
				Reps:       heaviest.Reps,
				Unit:       heaviest.Unit,
				AchievedAt: snap.PerformedAt,
			}
		}
	}

	return current, events
}

// ExercisesInWorkout returns the distinct exercise IDs present in a
// workout, in first-occurrence order. Used by the write path to scope
// which (user, exercise) PRs an incoming or edited workout could
// possibly affect.
func ExercisesInWorkout(w Workout) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, e := range w.Exercises {
		if _, ok := seen[e.ExerciseID]; ok {
			continue
		}
		seen[e.ExerciseID] = struct{}{}
		out = append(out, e.ExerciseID)
	}
	return out
}

// ToSnapshot projects a Workout into the minimal shape RecomputePR
// consumes. Pulled out so the live write path and the backfill share
// the same projection.
func ToSnapshot(w Workout) WorkoutSnapshot {
	exs := make([]ExerciseSnapshot, len(w.Exercises))
	for i, e := range w.Exercises {
		exs[i] = ExerciseSnapshot{
			ExerciseID: e.ExerciseID,
			Sets:       e.Sets,
		}
	}
	return WorkoutSnapshot{
		ID:          w.ID,
		UserID:      w.UserID,
		PerformedAt: w.PerformedAt,
		Exercises:   exs,
	}
}
