package workout

import (
	"math"
	"sort"
	"time"
)

// Progression is the response body for GET /workouts/progression. It
// captures, for a single exercise across a date range, one data point
// per workout (avg/min/max estimated 1RM across that workout's sets)
// plus least-squares trendlines fit through each series. The frontend
// uses this to plot a chart that visually answers "am I getting
// stronger?" — a positive-slope trendline says yes, flat says
// stagnating, negative says regressing.
type Progression struct {
	ExerciseID string    `json:"exercise_id"`
	Since      time.Time `json:"since"`
	Until      time.Time `json:"until"`
	// Unit chosen for the chart's Y-axis. We pick the most-common unit
	// across the queried sets and drop the others — see ComputeProgression
	// for details. Empty when no points exist.
	Unit string `json:"unit,omitempty"`
	// Count of sets that were excluded because their unit didn't match
	// the chosen dominant unit. The frontend can surface this as a
	// "N sets in <other unit> excluded" note if non-zero.
	SkippedOtherUnitCount int                `json:"skipped_other_unit_count"`
	Points                []ProgressionPoint `json:"points"`
	// Trendlines are nil when there's <2 points or when all points
	// fall on the same X (e.g., one day) so the regression is
	// undefined. The frontend should hide the lines in that case.
	TrendlineAvg *Trendline `json:"trendline_avg,omitempty"`
	TrendlineMax *Trendline `json:"trendline_max,omitempty"`
	TrendlineMin *Trendline `json:"trendline_min,omitempty"`
}

// ProgressionPoint is one workout's contribution to the chart —
// estimated 1RM aggregated across all of that workout's sets for the
// queried exercise.
type ProgressionPoint struct {
	WorkoutID       string    `json:"workout_id"`
	PerformedAt     time.Time `json:"performed_at"`
	AvgEstimated1RM float64   `json:"avg_estimated_1rm"`
	MaxEstimated1RM float64   `json:"max_estimated_1rm"`
	MinEstimated1RM float64   `json:"min_estimated_1rm"`
	// SetCount is how many sets contributed after the unit filter.
	// Useful for the frontend tooltip ("4 sets averaged at …").
	SetCount int `json:"set_count"`
}

// Trendline is two endpoints on the least-squares line, evaluated at
// the query's `since` and `until`. Returning ready-to-plot endpoints
// (rather than slope/intercept) means the frontend can render the line
// with two coordinates without re-deriving the math.
type Trendline struct {
	StartAt    time.Time `json:"start_at"`
	StartValue float64   `json:"start_value"`
	EndAt      time.Time `json:"end_at"`
	EndValue   float64   `json:"end_value"`
}

// EpleyOneRM returns the Epley estimated 1RM for a set:
//
//	1RM = weight × (1 + reps/30)
//
// We picked Epley over Brzycki for two reasons: it's the most widely
// used in lifting apps + literature (familiarity), and it stays well-
// defined down to 1 rep where Brzycki has a finite point but a
// discontinuity at reps=37. For reps=1 the formula collapses to the
// raw weight, which matches reality — a 1-rep set IS a 1RM. For very
// high reps (>10-12) the estimate is noisy across all formulas; we
// don't filter those out today and accept some variance in the trend.
func EpleyOneRM(weight float64, reps int) float64 {
	if reps <= 1 {
		return weight
	}
	return weight * (1.0 + float64(reps)/30.0)
}

// ComputeProgression analyzes the given workouts and returns the
// per-workout aggregates + trendlines for the specified exercise.
//
// Algorithm:
//  1. First pass: count units (lb vs kg) across every matching set.
//     Pick the dominant unit; if there's a tie, lb wins (arbitrary
//     but stable across calls).
//  2. Second pass: for each workout, gather sets matching the dominant
//     unit, compute Epley 1RM per set, then take avg/min/max across
//     those values. Workouts with zero matching sets are skipped.
//  3. Sort points ascending by performed_at so chart rendering is
//     ordered correctly even if the input wasn't.
//  4. Run least-squares regression on each of avg/min/max series.
//     Evaluate at since/until for plot endpoints. Skip trendlines if
//     fewer than 2 points or zero X-variance (all on the same day).
func ComputeProgression(
	workouts []Workout,
	exerciseID string,
	since time.Time,
	until time.Time,
) Progression {
	result := Progression{
		ExerciseID: exerciseID,
		Since:      since,
		Until:      until,
		Points:     []ProgressionPoint{},
	}

	// --- 1) Determine the dominant unit ---
	unitCounts := map[string]int{}
	for i := range workouts {
		for _, we := range workouts[i].Exercises {
			if we.ExerciseID != exerciseID {
				continue
			}
			for _, s := range we.Sets {
				unitCounts[string(s.Unit)]++
			}
		}
	}
	// Tie-breaking: prefer lb over kg, otherwise lexicographic. This
	// avoids non-deterministic responses for the same input across
	// Go's map iteration order.
	var dominantUnit string
	maxCount := 0
	for unit, count := range unitCounts {
		if count > maxCount ||
			(count == maxCount && (unit == "lb" || (dominantUnit != "lb" && unit < dominantUnit))) {
			dominantUnit = unit
			maxCount = count
		}
	}
	result.Unit = dominantUnit

	// --- 2) Per-workout aggregates ---
	for i := range workouts {
		w := workouts[i]
		var rms []float64
		// A single workout can technically list the same exercise
		// twice (e.g., warmup block + main block). Combine all
		// matching sets into one stats bucket for the workout so the
		// chart still shows one point per session.
		for _, we := range w.Exercises {
			if we.ExerciseID != exerciseID {
				continue
			}
			for _, s := range we.Sets {
				if string(s.Unit) != dominantUnit {
					result.SkippedOtherUnitCount++
					continue
				}
				rms = append(rms, EpleyOneRM(s.Weight, s.Reps))
			}
		}
		if len(rms) == 0 {
			continue
		}
		avg, mn, mx := stats(rms)
		result.Points = append(result.Points, ProgressionPoint{
			WorkoutID:       w.ID,
			PerformedAt:     w.PerformedAt,
			AvgEstimated1RM: round1(avg),
			MaxEstimated1RM: round1(mx),
			MinEstimated1RM: round1(mn),
			SetCount:        len(rms),
		})
	}

	// --- 3) Sort points by date ---
	sort.Slice(result.Points, func(i, j int) bool {
		return result.Points[i].PerformedAt.Before(result.Points[j].PerformedAt)
	})

	// --- 4) Trendlines ---
	if len(result.Points) >= 2 {
		result.TrendlineAvg = regressionLine(result.Points, since, until,
			func(p ProgressionPoint) float64 { return p.AvgEstimated1RM })
		result.TrendlineMax = regressionLine(result.Points, since, until,
			func(p ProgressionPoint) float64 { return p.MaxEstimated1RM })
		result.TrendlineMin = regressionLine(result.Points, since, until,
			func(p ProgressionPoint) float64 { return p.MinEstimated1RM })
	}

	return result
}

// stats returns mean / min / max in a single pass over values. Assumes
// values is non-empty (the caller has already checked).
func stats(values []float64) (mean, min, max float64) {
	min = values[0]
	max = values[0]
	sum := 0.0
	for _, v := range values {
		sum += v
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	mean = sum / float64(len(values))
	return
}

// round1 rounds to one decimal place. Helps the JSON output stay
// readable ("232.7" vs "232.66666666666669") without losing precision
// the user would actually notice.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// regressionLine fits a least-squares line through points using the
// provided Y extractor (avg/min/max of the 1RM estimate), evaluates it
// at `since` and `until`, and returns those two endpoints. Returns nil
// when fewer than 2 points or when X-variance is zero (e.g., every
// workout falls on the same day, which makes the slope undefined).
//
// X is unix milliseconds. Using floats over that range is fine — JS
// Number handles it exactly and Go float64 has way more headroom.
func regressionLine(
	points []ProgressionPoint,
	since time.Time,
	until time.Time,
	getY func(ProgressionPoint) float64,
) *Trendline {
	n := len(points)
	if n < 2 {
		return nil
	}

	// Explicit zero-variance check on X. Float math on unix-millis-
	// scale numbers (~1.7e12) squared loses precision past float64's
	// 15-digit limit, so checking the denominator against 0 after the
	// fact is unreliable — for identical X values, denom comes out as
	// some tiny non-zero residue and the slope blows up. Comparing
	// integers up front is exact.
	firstX := points[0].PerformedAt.UnixMilli()
	allSameX := true
	for i := 1; i < n; i++ {
		if points[i].PerformedAt.UnixMilli() != firstX {
			allSameX = false
			break
		}
	}
	if allSameX {
		return nil
	}

	var sumX, sumY, sumXY, sumXX float64
	for _, p := range points {
		x := float64(p.PerformedAt.UnixMilli())
		y := getY(p)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := float64(n)*sumXX - sumX*sumX
	if denom <= 0 {
		// Belt-and-braces — the explicit X-variance check above
		// already covers the degenerate case, but a non-positive
		// denominator here means we've lost so much precision that
		// the result wouldn't be trustworthy anyway.
		return nil
	}
	slope := (float64(n)*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / float64(n)

	startX := float64(since.UnixMilli())
	endX := float64(until.UnixMilli())
	return &Trendline{
		StartAt:    since,
		StartValue: round1(slope*startX + intercept),
		EndAt:      until,
		EndValue:   round1(slope*endX + intercept),
	}
}
