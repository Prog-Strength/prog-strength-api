package dashboard

import (
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

// buildLifting assembles the lifting tile from already-fetched workouts plus
// the precomputed PR count and headline 1RM (both supplied by the caller,
// since they come from other queries). Pure: now and loc drive the local-week
// bucketing deterministically. Returns nil when there are no workouts.
func buildLifting(workouts []workout.Workout, prCount int, headline *Headline1RM, unit string, now time.Time, loc *time.Location) *LiftingSection {
	if len(workouts) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}

	section := &LiftingSection{
		CurrentWeek:          liftingCurrentWeek(workouts, prCount, now, loc),
		HeadlineEstimated1RM: headline,
		WeeklyVolumeSpark:    weeklyVolumeSpark(workouts, now, loc),
		Unit:                 unit,
	}
	return section
}

// liftingCurrentWeek rolls up the workouts performed this local week. Duration
// only counts sessions with an EndedAt (skip nil); sets is the total across
// all exercises; prs is the caller-supplied count.
func liftingCurrentWeek(workouts []workout.Workout, prCount int, now time.Time, loc *time.Location) LiftingCurrentWeek {
	current := localWeekStart(now, loc)
	cw := LiftingCurrentWeek{PRs: prCount}
	for i := range workouts {
		if !localWeekStart(workouts[i].PerformedAt, loc).Equal(current) {
			continue
		}
		cw.Sessions++
		cw.Sets += totalSets(workouts[i])
		if workouts[i].EndedAt != nil {
			cw.DurationSeconds += int(workouts[i].EndedAt.Sub(workouts[i].PerformedAt).Seconds())
		}
	}
	return cw
}

// weeklyVolumeSpark sums lifting volume (Σ weight×reps) into each of the last
// sparkWeeks local weeks, oldest→newest, zero-filling empty weeks.
func weeklyVolumeSpark(workouts []workout.Workout, now time.Time, loc *time.Location) []float64 {
	starts := weeklyBucketStarts(now, loc, sparkWeeks)
	spark := make([]float64, len(starts))
	oldest := starts[0]
	for i := range workouts {
		ws := localWeekStart(workouts[i].PerformedAt, loc)
		if ws.Before(oldest) {
			continue
		}
		if idx := weekIndex(starts, ws); idx >= 0 {
			spark[idx] += workoutVolume(workouts[i])
		}
	}
	return spark
}

func totalSets(w workout.Workout) int {
	n := 0
	for i := range w.Exercises {
		n += len(w.Exercises[i].Sets)
	}
	return n
}

func workoutVolume(w workout.Workout) float64 {
	var vol float64
	for i := range w.Exercises {
		for _, s := range w.Exercises[i].Sets {
			vol += s.Weight * float64(s.Reps)
		}
	}
	return vol
}
