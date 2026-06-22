package snapshot

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

const maxTopSetsPerSession = 3

// bodyweightReading is the snapshot's internal projection of a bodyweight
// entry, keeping aggregate.go free of the bodyweight repo import. The
// service (Task A2) maps bodyweight.Entry into this shape; it lives here
// rather than service.go so the aggregation tests can construct it before
// the service exists.
type bodyweightReading struct {
	measuredAt time.Time
	weight     float64
	unit       string
}

// aggregateStrength rolls up the window's workouts into the strength
// section. Always returns a non-nil section (empty input → zeroed
// section) so an empty-but-healthy domain renders, not nulls.
func aggregateStrength(
	workouts []workout.Workout,
	prEvents []workout.PersonalRecordEvent,
	exercises []exercise.Exercise,
	unit string,
	loc *time.Location,
) *StrengthSection {
	muscleByExercise := map[string][]exercise.MuscleGroup{}
	nameByExercise := map[string]string{}
	for _, e := range exercises {
		muscleByExercise[e.ID] = e.MuscleGroups
		nameByExercise[e.ID] = e.Name
	}
	prsByWorkout := map[string][]workout.PersonalRecordEvent{}
	for _, ev := range prEvents {
		prsByWorkout[ev.WorkoutID] = append(prsByWorkout[ev.WorkoutID], ev)
	}

	sec := &StrengthSection{Unit: unit, ByMuscleGroup: []MuscleGroupVolume{},
		Sessions: []StrengthSession{}, HeadlinePRs: []string{}}
	sec.SessionCount = len(workouts)

	type mg struct {
		sets   int
		volume float64
	}
	mgTotals := map[exercise.MuscleGroup]*mg{}

	for _, w := range workouts {
		session := StrengthSession{
			Date: w.PerformedAt.In(loc).Format("2006-01-02"),
			Name: w.Name, TopSets: []TopSet{}, PRs: []SessionPR{},
		}
		var bestByExercise []TopSet
		for _, we := range w.Exercises {
			setCount := len(we.Sets)
			var exVol float64
			var best TopSet
			haveBest := false
			for _, s := range we.Sets {
				exVol += s.Weight * float64(s.Reps)
				sec.TotalVolume += s.Weight * float64(s.Reps)
				est := workout.EpleyOneRM(s.Weight, s.Reps)
				if !haveBest || est > best.Est1RM {
					best = TopSet{Exercise: we.ExerciseID, Weight: s.Weight, Reps: s.Reps, Est1RM: roundTo(est, 0)}
					haveBest = true
				}
			}
			if haveBest {
				bestByExercise = append(bestByExercise, best)
			}
			for _, g := range muscleByExercise[we.ExerciseID] {
				if mgTotals[g] == nil {
					mgTotals[g] = &mg{}
				}
				mgTotals[g].sets += setCount
				mgTotals[g].volume += exVol
			}
		}
		sort.SliceStable(bestByExercise, func(i, j int) bool {
			return bestByExercise[i].Est1RM > bestByExercise[j].Est1RM
		})
		if len(bestByExercise) > maxTopSetsPerSession {
			bestByExercise = bestByExercise[:maxTopSetsPerSession]
		}
		session.TopSets = append(session.TopSets, bestByExercise...)
		for _, ev := range prsByWorkout[w.ID] {
			session.PRs = append(session.PRs, SessionPR{Exercise: ev.ExerciseID, Kind: "weight"})
			name := nameByExercise[ev.ExerciseID]
			if name == "" {
				name = ev.ExerciseID
			}
			sec.HeadlinePRs = append(sec.HeadlinePRs,
				fmt.Sprintf("%s %s %s PR", trimFloat(ev.Weight), unit, name))
		}
		sec.Sessions = append(sec.Sessions, session)
	}

	for g, t := range mgTotals {
		sec.ByMuscleGroup = append(sec.ByMuscleGroup, MuscleGroupVolume{
			MuscleGroup: string(g), Sets: t.sets, Volume: t.volume,
		})
	}
	sort.SliceStable(sec.ByMuscleGroup, func(i, j int) bool {
		return sec.ByMuscleGroup[i].Volume > sec.ByMuscleGroup[j].Volume
	})
	return sec
}

// aggregateRunning filters to running activities, totals distance and
// duration, derives the section pace from those totals (nil when no
// distance was logged), and flags best efforts whose source activity
// started inside the window. Always non-nil.
func aggregateRunning(
	activities []activity.Activity,
	bests []activity.RunningBestEffort,
	start, end time.Time,
	loc *time.Location,
) *RunningSection {
	sec := &RunningSection{Runs: []RunSummary{}, NewBestEfforts: []BestEffort{}}
	var totalDist float64
	var totalDur int
	for _, a := range activities {
		if a.ActivityType != activity.ActivityRunning {
			continue
		}
		sec.RunCount++
		totalDist += a.DistanceMeters
		totalDur += a.DurationSeconds
		name := ""
		if a.Name != nil {
			name = *a.Name
		}
		sec.Runs = append(sec.Runs, RunSummary{
			Date: a.StartTime.In(loc).Format("2006-01-02"), Name: name,
			DistanceM: a.DistanceMeters, DurationS: a.DurationSeconds,
			AvgPaceSecPerKm: a.AvgPaceSecPerKm,
		})
	}
	sec.TotalDistanceM = totalDist
	sec.TotalDurationS = totalDur
	if totalDist > 0 {
		pace := float64(totalDur) / (totalDist / 1000.0)
		sec.AvgPaceSecPerKm = &pace
	}
	for _, b := range bests {
		if !b.ActivityStartTime.Before(start) && b.ActivityStartTime.Before(end) {
			sec.NewBestEfforts = append(sec.NewBestEfforts, BestEffort{
				DistanceKey: b.DistanceKey, TimeSeconds: int(math.Round(b.DurationSeconds)),
			})
		}
	}
	return sec
}

// aggregateSteps totals the window's step entries. days_logged and the
// average count only days with steps > 0; total sums every entry. Always
// non-nil.
func aggregateSteps(entries []steps.Entry, goal int, _ *time.Location) *StepsSection {
	sec := &StepsSection{Goal: goal, ByDay: []StepsDay{}}
	var sum int
	for _, e := range entries {
		sec.ByDay = append(sec.ByDay, StepsDay{Date: e.Date, Steps: e.Steps})
		if e.Steps > 0 {
			sec.DaysLogged++
			sum += e.Steps
		}
		sec.Total += e.Steps
	}
	sort.SliceStable(sec.ByDay, func(i, j int) bool { return sec.ByDay[i].Date < sec.ByDay[j].Date })
	if sec.DaysLogged > 0 {
		sec.Avg = int(math.Round(float64(sum) / float64(sec.DaysLogged)))
	}
	return sec
}

// aggregateBodyweight orders readings oldest → newest and reports the
// first as start, the last as end, and their signed difference as delta.
// Always non-nil; an empty input yields a zeroed section.
func aggregateBodyweight(entries []bodyweightReading) *BodyweightSection {
	sec := &BodyweightSection{Readings: []BodyweightReading{}}
	if len(entries) == 0 {
		return sec
	}
	sorted := make([]bodyweightReading, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].measuredAt.Before(sorted[j].measuredAt) })
	sec.Unit = sorted[0].unit
	sec.Start = sorted[0].weight
	sec.End = sorted[len(sorted)-1].weight
	sec.Delta = roundTo(sec.End-sec.Start, 1)
	for _, r := range sorted {
		sec.Readings = append(sec.Readings, BodyweightReading{
			Date: r.measuredAt.Format("2006-01-02"), Weight: r.weight,
		})
	}
	return sec
}

// aggregateNutrition averages the logged days' macros (rounded to int),
// passes goals through, and emits each day rounded. days_logged is the
// number of rows the repo returned. Always non-nil.
func aggregateNutrition(days []nutrition.DailyMacros, goals nutrition.MacroGoals) *NutritionSection {
	sec := &NutritionSection{ByDay: []NutritionDay{},
		Goals: MacroSet{Calories: goals.Calories, ProteinG: goals.ProteinG, FatG: goals.FatG, CarbsG: goals.CarbsG}}
	sec.DaysLogged = len(days)
	var c, p, f, cb float64
	for _, d := range days {
		c += d.Calories
		p += d.ProteinG
		f += d.FatG
		cb += d.CarbsG
		sec.ByDay = append(sec.ByDay, NutritionDay{Date: d.Date,
			Calories: int(math.Round(d.Calories)), ProteinG: int(math.Round(d.ProteinG)),
			FatG: int(math.Round(d.FatG)), CarbsG: int(math.Round(d.CarbsG))})
	}
	sort.SliceStable(sec.ByDay, func(i, j int) bool { return sec.ByDay[i].Date < sec.ByDay[j].Date })
	if sec.DaysLogged > 0 {
		n := float64(sec.DaysLogged)
		sec.Avg = MacroSet{Calories: int(math.Round(c / n)), ProteinG: int(math.Round(p / n)),
			FatG: int(math.Round(f / n)), CarbsG: int(math.Round(cb / n))}
	}
	return sec
}

// countActiveDays is the number of distinct local days on which the user
// did anything: a logged workout, a run, or a non-zero step day. Nil
// sections (a failed domain) contribute nothing rather than panicking.
func countActiveDays(strength *StrengthSection, running *RunningSection, stepsSec *StepsSection) int {
	active := map[string]bool{}
	if strength != nil {
		for _, s := range strength.Sessions {
			active[s.Date] = true
		}
	}
	if running != nil {
		for _, r := range running.Runs {
			active[r.Date] = true
		}
	}
	if stepsSec != nil {
		for _, d := range stepsSec.ByDay {
			if d.Steps > 0 {
				active[d.Date] = true
			}
		}
	}
	return len(active)
}

// roundTo rounds v to the given number of decimal places.
func roundTo(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// trimFloat formats a weight for a headline PR string, dropping a
// trailing ".0" so "335 lb" reads naturally rather than "335.0 lb".
func trimFloat(v float64) string {
	return fmt.Sprintf("%g", roundTo(v, 1))
}
