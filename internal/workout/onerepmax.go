package workout

import (
	"math"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// OneRepMaxEntry is a single row of the exercise_one_rep_max_history
// table — one (workout, exercise) pair summarized as min/avg/max Epley
// estimated 1RMs across that exercise's sets in that workout. This is
// the unit of storage for every downstream "am I getting stronger"
// analysis. See prog-strength-docs/sows/estimated-one-rep-max-time-
// series-table.md for the full design rationale.
type OneRepMaxEntry struct {
	ID              string
	UserID          string
	WorkoutID       string
	ExerciseID      string
	PerformedAt     time.Time
	MinEstimated1RM float64
	AvgEstimated1RM float64
	MaxEstimated1RM float64
	SetCount        int
	Unit            user.WeightUnit
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AggregateOneRepMax produces one OneRepMaxEntry per distinct exercise
// in the workout. Pure function — no IO, no time.Now, no ID generation;
// ID/CreatedAt/UpdatedAt are left zero for the repository to fill so
// the live write path and the backfill share identical aggregation.
//
// Behavior matches the existing /workouts/progression handler:
//
//   - Multiple WorkoutExercise blocks with the same ExerciseID (e.g.
//     warmup + main) are folded into a single entry.
//   - Unit reconciliation is "most-common wins, lb on tie." Sets in the
//     minority unit are dropped from min/avg/max.
//   - An exercise with zero sets in the dominant unit produces no entry.
func AggregateOneRepMax(w Workout) []OneRepMaxEntry {
	// Fold same-ExerciseID blocks. Preserve first-occurrence order so
	// the returned slice is deterministic for the same input regardless
	// of how the exercises were ordered inside the workout.
	type bucket struct {
		sets []Set
	}
	buckets := map[string]*bucket{}
	var order []string
	for _, e := range w.Exercises {
		b, ok := buckets[e.ExerciseID]
		if !ok {
			b = &bucket{}
			buckets[e.ExerciseID] = b
			order = append(order, e.ExerciseID)
		}
		b.sets = append(b.sets, e.Sets...)
	}

	var out []OneRepMaxEntry
	for _, eid := range order {
		sets := buckets[eid].sets
		if len(sets) == 0 {
			continue
		}
		unit := dominantUnit(sets)

		var rms []float64
		for _, s := range sets {
			if s.Unit != unit {
				continue
			}
			rms = append(rms, EpleyOneRM(s.Weight, s.Reps))
		}
		if len(rms) == 0 {
			continue
		}

		mn, mx := rms[0], rms[0]
		sum := 0.0
		for _, v := range rms {
			if v < mn {
				mn = v
			}
			if v > mx {
				mx = v
			}
			sum += v
		}

		out = append(out, OneRepMaxEntry{
			UserID:          w.UserID,
			WorkoutID:       w.ID,
			ExerciseID:      eid,
			PerformedAt:     w.PerformedAt,
			MinEstimated1RM: mn,
			AvgEstimated1RM: sum / float64(len(rms)),
			MaxEstimated1RM: mx,
			SetCount:        len(rms),
			Unit:            unit,
		})
	}
	return out
}

// dominantUnit returns the most-common unit across the given sets, with
// lb winning on tie. This matches the tie-breaking in ComputeProgression
// so the two read paths produce consistent results for the same data.
func dominantUnit(sets []Set) user.WeightUnit {
	var lb, kg int
	for _, s := range sets {
		switch s.Unit {
		case user.WeightUnitPounds:
			lb++
		case user.WeightUnitKilograms:
			kg++
		}
	}
	if kg > lb {
		return user.WeightUnitKilograms
	}
	return user.WeightUnitPounds
}

// Default tuning constants for the recency-weighted baseline, per the
// SOW. Both are tunable starting points to validate against beta data,
// not load-bearing assumptions.
const (
	// DefaultBaselineWindow is the maximum age of an entry that
	// contributes to the baseline. Entries older than this are dropped
	// entirely — three months out is no longer "current capability."
	DefaultBaselineWindow = 90 * 24 * time.Hour

	// DefaultBaselineTau is the time constant of the exponential decay
	// weighting. A 45-day tau gives a half-life of about 31 days — each
	// successive month contributes roughly half the weight of the prior
	// month, which keeps a one-week deload from dominating the baseline.
	DefaultBaselineTau = 45 * 24 * time.Hour
)

// RecencyWeightedBaseline returns the time-weighted moving average of
// entries.MaxEstimated1RM as of `at`, using the exponential decay
//
//	weight = exp( -(at - performed_at) / tau )
//
// for each entry whose performed_at lies in the window (at-window, at].
// The second return value is false when no entry falls inside the
// window — callers can choose to render an empty state.
//
// The per-workout MAX is used (not avg) because the avg across a
// workout's sets is dragged down by warmup sets, which has nothing to
// do with the lifter's actual capability. Max is almost always one of
// the working sets, so it tracks the load that answers "what could
// this person do today?" without polluting the signal with warmup
// protocol drift. A future warmup-flag-on-sets feature can replace
// this with "working-set average" and the function signature won't
// change.
//
// Pure function: no time.Now, no IO. Exhaustively testable and shared
// by every consumer of the baseline so future tuning lives in one place.
func RecencyWeightedBaseline(
	entries []OneRepMaxEntry,
	at time.Time,
	window time.Duration,
	tau time.Duration,
) (float64, bool) {
	cutoff := at.Add(-window)
	var weightedSum, weightSum float64
	for _, e := range entries {
		// Entries outside (cutoff, at] don't contribute. The lower bound
		// is exclusive so a fresh entry at the cutoff doesn't appear and
		// disappear at the boundary as time advances.
		if !e.PerformedAt.After(cutoff) || e.PerformedAt.After(at) {
			continue
		}
		age := at.Sub(e.PerformedAt)
		w := math.Exp(-float64(age) / float64(tau))
		weightedSum += w * e.MaxEstimated1RM
		weightSum += w
	}
	if weightSum == 0 {
		return 0, false
	}
	return weightedSum / weightSum, true
}

// DownsampleEstimated1RM returns a compact ascending (oldest→newest)
// slice of rounded MaxEstimated1RM values for a per-tile trend spark.
//
// entries are expected DESC by PerformedAt (as ListOneRepMaxHistory
// returns them); the result is reversed to ascending. When more than
// maxPoints entries are present they are downsampled to exactly
// maxPoints points by even stride, always retaining the oldest and
// newest. Returns nil for empty input or a non-positive maxPoints so the
// JSON field renders as null.
//
// Pure: no IO, no time.Now — a downsample of values already in hand, not
// a new series. The estimated-1RM math is unchanged; this only thins it.
func DownsampleEstimated1RM(entries []OneRepMaxEntry, maxPoints int) []float64 {
	if len(entries) == 0 || maxPoints <= 0 {
		return nil
	}

	// Reverse to ascending: entries[0] is newest.
	asc := make([]float64, len(entries))
	for i, e := range entries {
		asc[len(entries)-1-i] = round1(e.MaxEstimated1RM)
	}

	if len(asc) <= maxPoints {
		return asc
	}

	if maxPoints == 1 {
		// A one-point spark is just the latest estimate; the stride
		// formula below divides by maxPoints-1, so handle it here.
		return []float64{asc[len(asc)-1]}
	}

	n := len(asc)
	out := make([]float64, maxPoints)
	for i := 0; i < maxPoints; i++ {
		// Even stride across [0, n-1] inclusive of both ends.
		idx := int(math.Round(float64(i) * float64(n-1) / float64(maxPoints-1)))
		out[i] = asc[idx]
	}
	return out
}
