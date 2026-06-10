package workout

import (
	"sort"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
)

// baselineModelRecencyWeighted is the discriminator the UI uses to label
// what a normalized value of 1.0 ("100%") means. It names the baseline
// math (RecencyWeightedBaseline at `now`). If we ever swap the model
// this value — and the UI's label — change together.
const baselineModelRecencyWeighted = "recency_weighted_current"

// Progression aggregate / per-exercise tuning constants. See
// prog-strength-docs/sows/progress-page-modernization.md § Algorithms.
const (
	// minSessionsThreshold is the minimum number of in-window sessions an
	// exercise needs before we fit a per-exercise trend. Below it the
	// dots still render but slope/trendline are null and the exercise is
	// excluded from the aggregate.
	minSessionsThreshold = 3

	// progressingSlopeThreshold is the noise floor (percentage points per
	// month) a lift's slope must clear to count as "progressing." The
	// frontend's stat-card tone logic uses the same constant so the two
	// agree on what "progressing" means.
	progressingSlopeThreshold = 0.25

	// monthHours is the number of hours in an average month (30.4375
	// days), used to scale the per-exercise regression X-axis to months
	// so slope_per_month is the regression slope itself.
	monthHours = 24 * 30.4375
)

// MuscleGroupProgression is the response body for GET
// /workouts/muscle-group-progression. It normalizes per-(workout,
// exercise) estimated 1RM history against each exercise's current
// recency-weighted baseline so disparate exercises within a muscle
// group can be plotted on a single comparable Y-axis.
//
// See prog-strength-docs/sows/estimated-one-rep-max.md
// for the full design rationale. The short version: a lifter's chest
// strength is a single thing, but the absolute 1RM on barbell bench
// vs dumbbell bench vs cable fly lives on different scales. Dividing
// each session's 1RM by that exercise's current baseline turns it
// into a fraction of the lifter's current capability on that exercise
// — a number that means the same thing across every exercise.
type MuscleGroupProgression struct {
	// Filter echoes the requested filter (movement pattern or single
	// muscle group) plus the resolved set of muscle groups behind it so
	// the UI can render a "Showing chest, shoulders, triceps" caption
	// without duplicating the rollup.
	Filter ProgressionFilterResponse `json:"filter"`

	Since time.Time `json:"since"`
	Until time.Time `json:"until"`

	// BaselineModel is the discriminator the UI uses to label what a
	// normalized value of 1.0 means. Always "recency_weighted_current"
	// today; see baselineModelRecencyWeighted.
	BaselineModel string `json:"baseline_model"`

	// ExerciseBaselines lists, for each exercise that contributed at
	// least one point, the current recency-weighted 1RM baseline used
	// for normalization. The frontend uses these for tooltip context
	// and chart legends. Sorted by exercise_name for stable rendering.
	ExerciseBaselines []ExerciseBaseline `json:"exercise_baselines"`

	// Points is one entry per (workout, exercise) pair where the
	// exercise targets this filter and a baseline could be computed.
	// Sorted by performed_at ascending so charts render left-to-right
	// without re-sorting client-side.
	Points []MuscleGroupProgressionPoint `json:"points"`

	// PerExerciseTrends carries one entry per exercise that contributed
	// at least one point, each with its in-window session count and (when
	// it clears minSessionsThreshold and isn't degenerate) a per-month
	// slope and trendline. Replaces the old single cross-exercise
	// trendline, which mixed exercises on different baselines.
	PerExerciseTrends []PerExerciseTrend `json:"per_exercise_trends"`

	// Aggregate rolls the per-exercise trends up into the page's headline
	// stat cards.
	Aggregate ProgressionAggregate `json:"aggregate"`
}

// ProgressionFilterResponse is the `filter` block of the response.
// Exactly one of MovementPattern / MuscleGroup is populated (the other
// is omitted via omitempty); MuscleGroupsIncluded is always the resolved
// list.
type ProgressionFilterResponse struct {
	MovementPattern      string   `json:"movement_pattern,omitempty"`
	MuscleGroup          string   `json:"muscle_group,omitempty"`
	MuscleGroupsIncluded []string `json:"muscle_groups_included"`
}

// ProgressionFilter is the filter descriptor passed into
// ComputeMuscleGroupProgression. The handler builds it after resolving
// the request's movement_pattern or muscle_group param; the compute
// function only reads it to populate the response `filter` block, so it
// stays a pure function of its inputs.
type ProgressionFilter struct {
	// MovementPattern is set (and MuscleGroup empty) on the pattern path;
	// MuscleGroup is set (and MovementPattern empty) on the legacy
	// single-muscle path.
	MovementPattern string
	MuscleGroup     string
	// MuscleGroups is the resolved set of muscle groups echoed as
	// muscle_groups_included.
	MuscleGroups []exercise.MuscleGroup
}

// PerExerciseTrend is one exercise's in-window trend. SlopePerMonth and
// Trendline are nil when SessionCount < minSessionsThreshold or the
// regression is degenerate (all points share the same X) — the renderer
// treats those as "not enough data" rather than fitting a line through
// two points.
type PerExerciseTrend struct {
	ExerciseID    string     `json:"exercise_id"`
	SessionCount  int        `json:"session_count"`
	SlopePerMonth *float64   `json:"slope_per_month"`
	Trendline     *Trendline `json:"trendline"`
}

// ProgressionAggregate is the response's headline rollup. Median is nil
// when no exercise clears the session threshold.
type ProgressionAggregate struct {
	LiftsTracked         int      `json:"lifts_tracked"`
	LiftsProgressing     int      `json:"lifts_progressing"`
	MedianSlopePerMonth  *float64 `json:"median_slope_per_month"`
	MinSessionsThreshold int      `json:"min_sessions_threshold"`
}

// ExerciseBaseline is the per-exercise context the frontend needs to
// explain normalized values to the user. Surfaced separately from
// Points so the tooltip can show "your set was at 92% of your current
// barbell bench press capability (~245 lb)" without the chart having
// to carry the baseline on every point.
type ExerciseBaseline struct {
	ExerciseID   string  `json:"exercise_id"`
	ExerciseName string  `json:"exercise_name"`
	Baseline     float64 `json:"baseline"`
	Unit         string  `json:"unit"`
}

// MuscleGroupProgressionPoint is one (workout, exercise) contribution
// to the chart. NormalizedMax is the load-bearing field (it's what the
// chart's Y-axis represents); the raw fields are carried for tooltip
// rendering so the frontend can show absolute numbers alongside the
// normalized percentage.
type MuscleGroupProgressionPoint struct {
	WorkoutID    string    `json:"workout_id"`
	ExerciseID   string    `json:"exercise_id"`
	ExerciseName string    `json:"exercise_name"`
	PerformedAt  time.Time `json:"performed_at"`

	// NormalizedMax = max_estimated_1rm / current baseline. 1.0 means
	// the lifter's heaviest set today matched their current baseline
	// capability on this exercise; >1.0 above, <1.0 below. Using max
	// (rather than the per-workout avg) keeps warmup sets from
	// deflating the signal — see RecencyWeightedBaseline for the
	// full rationale. This is what gets plotted.
	NormalizedMax float64 `json:"normalized_max"`

	// Raw per-set aggregates carried for the tooltip.
	AvgEstimated1RM float64 `json:"avg_estimated_1rm"`
	MaxEstimated1RM float64 `json:"max_estimated_1rm"`
	MinEstimated1RM float64 `json:"min_estimated_1rm"`
	SetCount        int     `json:"set_count"`
	Unit            string  `json:"unit"`
}

// ExerciseHistory pairs the read-side entries for one exercise with
// that exercise's display name. The handler builds this up from the
// catalog + repo queries; ComputeMuscleGroupProgression takes it as
// an opaque slice so the pure-math part has no IO dependency.
type ExerciseHistory struct {
	ExerciseID   string
	ExerciseName string
	// Entries spans a wide enough range to compute both the baseline
	// (which always looks at the last DefaultBaselineWindow) and the
	// chart points (which respect since/until). Filtering by
	// performed_at happens inside ComputeMuscleGroupProgression.
	Entries []OneRepMaxEntry
}

// ComputeMuscleGroupProgression turns per-exercise 1RM history into
// the normalized, charted progression for a filter (a movement pattern
// or a single muscle group).
//
// Algorithm:
//
//  1. For each exercise's entries, compute the recency-weighted
//     baseline as of `now` (not `until`) using DefaultBaselineWindow
//     and DefaultBaselineTau. The "current capability" anchor is the
//     present, not the end of the query window — a lifter querying
//     "last 90 days" expects to see how their past sat relative to
//     where they are *now*, not where they were three months ago.
//
//  2. Skip exercises without a computable baseline (no entries in the
//     last DefaultBaselineWindow). Without a baseline there is no
//     way to normalize, and showing raw 1RMs in a chart that's
//     supposed to mean "fraction of current capability" would mislead.
//
//  3. For each entry inside [since, until], emit one point with
//     normalized_max = max_estimated_1rm / baseline.
//
//  4. Fit a per-exercise least-squares trend on that exercise's own
//     normalized points (X scaled to months since `since`), so each
//     line stays on a single comparable baseline. Exercises with fewer
//     than minSessionsThreshold in-window sessions still emit points +
//     baselines (so the dots render) but get null slope/trendline and
//     are excluded from the aggregate.
//
//  5. Roll the per-exercise slopes up into the aggregate (lifts tracked,
//     lifts progressing, median slope).
//
// Pure function: no IO, no time.Now lookups. Caller supplies `now`
// so tests can pin the baseline calculation deterministically.
func ComputeMuscleGroupProgression(
	filter ProgressionFilter,
	histories []ExerciseHistory,
	since, until, now time.Time,
) MuscleGroupProgression {
	included := make([]string, 0, len(filter.MuscleGroups))
	for _, mg := range filter.MuscleGroups {
		included = append(included, string(mg))
	}

	result := MuscleGroupProgression{
		Filter: ProgressionFilterResponse{
			MovementPattern:      filter.MovementPattern,
			MuscleGroup:          filter.MuscleGroup,
			MuscleGroupsIncluded: included,
		},
		Since:             since,
		Until:             until,
		BaselineModel:     baselineModelRecencyWeighted,
		ExerciseBaselines: []ExerciseBaseline{},
		Points:            []MuscleGroupProgressionPoint{},
		PerExerciseTrends: []PerExerciseTrend{},
	}

	// Per-exercise points collected in the order exercises contributed,
	// so trends can be fit on each exercise's own series independently.
	type exerciseSeries struct {
		exerciseID string
		points     []MuscleGroupProgressionPoint
	}
	var series []exerciseSeries

	for _, h := range histories {
		baseline, ok := RecencyWeightedBaseline(
			h.Entries, now, DefaultBaselineWindow, DefaultBaselineTau,
		)
		if !ok || baseline <= 0 {
			continue
		}

		// Unit of the baseline is the most-recent in-window entry's
		// unit. In normal training that's stable per exercise; if a
		// user has mixed units within the window the value is still
		// meaningful since baselines and entries share the same unit.
		var baselineUnit string
		for _, e := range h.Entries {
			if !e.PerformedAt.Before(now.Add(-DefaultBaselineWindow)) {
				baselineUnit = string(e.Unit)
				break
			}
		}

		var exPoints []MuscleGroupProgressionPoint
		for _, e := range h.Entries {
			if e.PerformedAt.Before(since) || e.PerformedAt.After(until) {
				continue
			}
			p := MuscleGroupProgressionPoint{
				WorkoutID:       e.WorkoutID,
				ExerciseID:      e.ExerciseID,
				ExerciseName:    h.ExerciseName,
				PerformedAt:     e.PerformedAt,
				NormalizedMax:   round3(e.MaxEstimated1RM / baseline),
				AvgEstimated1RM: round1(e.AvgEstimated1RM),
				MaxEstimated1RM: round1(e.MaxEstimated1RM),
				MinEstimated1RM: round1(e.MinEstimated1RM),
				SetCount:        e.SetCount,
				Unit:            string(e.Unit),
			}
			result.Points = append(result.Points, p)
			exPoints = append(exPoints, p)
		}

		if len(exPoints) > 0 {
			result.ExerciseBaselines = append(result.ExerciseBaselines, ExerciseBaseline{
				ExerciseID:   h.ExerciseID,
				ExerciseName: h.ExerciseName,
				Baseline:     round1(baseline),
				Unit:         baselineUnit,
			})
			series = append(series, exerciseSeries{exerciseID: h.ExerciseID, points: exPoints})
		}
	}

	sort.Slice(result.Points, func(i, j int) bool {
		return result.Points[i].PerformedAt.Before(result.Points[j].PerformedAt)
	})
	sort.Slice(result.ExerciseBaselines, func(i, j int) bool {
		return result.ExerciseBaselines[i].ExerciseName < result.ExerciseBaselines[j].ExerciseName
	})

	// Per-exercise trends + aggregate. One pass over the per-exercise
	// series: fit the slope, build the trend entry, and (for exercises
	// that clear the session threshold) accumulate the aggregate.
	var qualifyingSlopes []float64
	liftsProgressing := 0
	for _, s := range series {
		trend := PerExerciseTrend{
			ExerciseID:   s.exerciseID,
			SessionCount: len(s.points),
		}
		if len(s.points) >= minSessionsThreshold {
			if slope, line, ok := perExerciseSlope(s.points, since, until); ok {
				slopePerMonth := round1(slope * 100)
				trend.SlopePerMonth = &slopePerMonth
				trend.Trendline = line
				qualifyingSlopes = append(qualifyingSlopes, slopePerMonth)
				if slopePerMonth > progressingSlopeThreshold {
					liftsProgressing++
				}
			}
		}
		result.PerExerciseTrends = append(result.PerExerciseTrends, trend)
	}

	result.Aggregate = ProgressionAggregate{
		LiftsTracked:         len(qualifyingSlopes),
		LiftsProgressing:     liftsProgressing,
		MedianSlopePerMonth:  medianOrNil(qualifyingSlopes),
		MinSessionsThreshold: minSessionsThreshold,
	}

	return result
}

// perExerciseSlope fits a least-squares line through one exercise's
// normalized points with X scaled to months elapsed since `since`, so
// the returned slope is the regression slope in normalized units per
// month (the caller multiplies by 100 for percentage points). The
// trendline endpoints are evaluated at since/until.
//
// Returns ok=false (and a nil trendline) when n < 2 or the X-variance
// is zero (all points share the same instant) — a degenerate fit the
// caller renders as "not enough data."
func perExerciseSlope(
	points []MuscleGroupProgressionPoint,
	since, until time.Time,
) (slope float64, line *Trendline, ok bool) {
	n := len(points)
	if n < 2 {
		return 0, nil, false
	}

	monthsSince := func(t time.Time) float64 {
		return t.Sub(since).Hours() / monthHours
	}

	var sumX, sumY, sumXY, sumXX float64
	for _, p := range points {
		x := monthsSince(p.PerformedAt)
		y := p.NormalizedMax
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := float64(n)*sumXX - sumX*sumX
	if denom == 0 {
		return 0, nil, false
	}
	slope = (float64(n)*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / float64(n)

	line = &Trendline{
		StartAt:    since,
		StartValue: round3(slope*monthsSince(since) + intercept),
		EndAt:      until,
		EndValue:   round3(slope*monthsSince(until) + intercept),
	}
	return slope, line, true
}

// medianOrNil returns a pointer to the median of vs, or nil when vs is
// empty. The median (not the mean) keeps one rocketing or tanking
// exercise from capturing the headline.
func medianOrNil(vs []float64) *float64 {
	n := len(vs)
	if n == 0 {
		return nil
	}
	sorted := make([]float64, n)
	copy(sorted, vs)
	sort.Float64s(sorted)
	var m float64
	if n%2 == 1 {
		m = sorted[n/2]
	} else {
		m = round1((sorted[n/2-1] + sorted[n/2]) / 2)
	}
	return &m
}

// round3 keeps normalized values readable in JSON ("0.927") without
// losing meaningful precision. Three decimals matches the granularity
// the chart actually uses on the Y-axis.
func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}
