package workout

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// chestFilter is the single-muscle filter the legacy tests run under.
var chestFilter = ProgressionFilter{
	MuscleGroup:  "chest",
	MuscleGroups: []exercise.MuscleGroup{exercise.MuscleChest},
}

// helper: build an OneRepMaxEntry with sensible defaults. All three
// per-set aggregates are set to the same `value` so the tests are
// agnostic about which one the baseline math reads — the actual
// production code uses MaxEstimated1RM.
func entry(performedAt time.Time, value float64) OneRepMaxEntry {
	return OneRepMaxEntry{
		WorkoutID:       "w-" + performedAt.Format("20060102"),
		ExerciseID:      "x",
		PerformedAt:     performedAt,
		MinEstimated1RM: value,
		AvgEstimated1RM: value,
		MaxEstimated1RM: value,
		SetCount:        3,
		Unit:            user.WeightUnitPounds,
	}
}

// trendForExercise returns the per-exercise trend entry for the given
// exercise ID, or nil when absent.
func trendForExercise(result MuscleGroupProgression, exerciseID string) *PerExerciseTrend {
	for i := range result.PerExerciseTrends {
		if result.PerExerciseTrends[i].ExerciseID == exerciseID {
			return &result.PerExerciseTrends[i]
		}
	}
	return nil
}

// analyticalSlopePerMonth independently recomputes slope_per_month for an
// exercise from the emitted normalized points — the same least-squares fit the
// implementation performs, with the `* 100` scaling hardcoded here so a change
// to the production scaling/formula is caught even when test inputs were sized
// against the implementation's own output (the probe-and-scale aggregate tests).
func analyticalSlopePerMonth(result MuscleGroupProgression, exerciseID string, since time.Time) float64 {
	var sx, sy, sxy, sxx, n float64
	for _, p := range result.Points {
		if p.ExerciseID != exerciseID {
			continue
		}
		x := p.PerformedAt.Sub(since).Hours() / monthHours
		y := p.NormalizedMax
		sx += x
		sy += y
		sxy += x * y
		sxx += x * x
		n++
	}
	slope := (n*sxy - sx*sy) / (n*sxx - sx*sx)
	return slope * 100
}

func TestComputeMuscleGroupProgression_Empty(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	now := until

	result := ComputeMuscleGroupProgression(chestFilter, nil, since, until, now)

	if result.Filter.MuscleGroup != "chest" {
		t.Errorf("filter.muscle_group: got %q, want chest", result.Filter.MuscleGroup)
	}
	if result.BaselineModel != "recency_weighted_current" {
		t.Errorf("baseline_model: got %q, want recency_weighted_current", result.BaselineModel)
	}
	if len(result.Points) != 0 {
		t.Errorf("expected 0 points, got %d", len(result.Points))
	}
	if len(result.ExerciseBaselines) != 0 {
		t.Errorf("expected 0 baselines, got %d", len(result.ExerciseBaselines))
	}
	if len(result.PerExerciseTrends) != 0 {
		t.Errorf("expected 0 per-exercise trends, got %d", len(result.PerExerciseTrends))
	}
	if result.Aggregate.LiftsTracked != 0 {
		t.Errorf("expected lifts_tracked 0, got %d", result.Aggregate.LiftsTracked)
	}
	if result.Aggregate.MedianSlopePerMonth != nil {
		t.Errorf("expected nil median for empty input")
	}
	if result.Aggregate.MinSessionsThreshold != 3 {
		t.Errorf("expected min_sessions_threshold 3, got %d", result.Aggregate.MinSessionsThreshold)
	}
}

func TestComputeMuscleGroupProgression_SkipsExerciseWithoutBaseline(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	// Only entry is 6 months old → outside the 90-day baseline window
	// → no baseline → exercise is skipped entirely.
	histories := []ExerciseHistory{{
		ExerciseID:   "stale-exercise",
		ExerciseName: "Stale Exercise",
		Entries:      []OneRepMaxEntry{entry(now.Add(-180*24*time.Hour), 200)},
	}}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	if len(result.Points) != 0 {
		t.Errorf("expected exercise with no baseline to be skipped; got %d points", len(result.Points))
	}
	if len(result.ExerciseBaselines) != 0 {
		t.Errorf("expected no baselines emitted; got %d", len(result.ExerciseBaselines))
	}
}

func TestComputeMuscleGroupProgression_SingleExerciseNormalized(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	// Three weekly entries all at the same value — the recency-weighted
	// baseline should equal that value, and the normalized point for
	// each entry should be exactly 1.0.
	histories := []ExerciseHistory{{
		ExerciseID:   "barbell-bench-press",
		ExerciseName: "Barbell Bench Press",
		Entries: []OneRepMaxEntry{
			entry(now.Add(-21*24*time.Hour), 200),
			entry(now.Add(-14*24*time.Hour), 200),
			entry(now.Add(-7*24*time.Hour), 200),
		},
	}}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	if len(result.Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(result.Points))
	}
	for _, p := range result.Points {
		if math.Abs(p.NormalizedMax-1.0) > 0.001 {
			t.Errorf("constant series should normalize to 1.0; got %v for %s", p.NormalizedMax, p.PerformedAt)
		}
	}
	if len(result.ExerciseBaselines) != 1 {
		t.Fatalf("expected 1 baseline, got %d", len(result.ExerciseBaselines))
	}
	if math.Abs(result.ExerciseBaselines[0].Baseline-200) > 0.5 {
		t.Errorf("baseline should equal the constant value; got %v", result.ExerciseBaselines[0].Baseline)
	}
}

func TestComputeMuscleGroupProgression_MultipleExercisesShareAxis(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	// Two different exercises with very different absolute strength
	// scales (barbell vs dumbbell). After normalization each entry
	// should sit at ~1.0 because each exercise's value is constant.
	histories := []ExerciseHistory{
		{
			ExerciseID:   "barbell-bench-press",
			ExerciseName: "Barbell Bench Press",
			Entries: []OneRepMaxEntry{
				entry(now.Add(-21*24*time.Hour), 250),
				entry(now.Add(-7*24*time.Hour), 250),
			},
		},
		{
			ExerciseID:   "dumbbell-bench-press",
			ExerciseName: "Dumbbell Bench Press",
			Entries: []OneRepMaxEntry{
				entry(now.Add(-14*24*time.Hour), 90),
				entry(now.Add(-3*24*time.Hour), 90),
			},
		},
	}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	if len(result.Points) != 4 {
		t.Fatalf("expected 4 points across both exercises, got %d", len(result.Points))
	}
	for _, p := range result.Points {
		if math.Abs(p.NormalizedMax-1.0) > 0.01 {
			t.Errorf("normalized value should be ~1.0 regardless of exercise scale; got %v for %s", p.NormalizedMax, p.ExerciseName)
		}
	}
}

func TestComputeMuscleGroupProgression_PointsSortedByTime(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	// Two exercises with entries interleaved in time; the final point
	// list should be globally sorted ascending so the chart renders
	// left-to-right without further sorting on the client.
	histories := []ExerciseHistory{
		{
			ExerciseID:   "a",
			ExerciseName: "A",
			Entries: []OneRepMaxEntry{
				entry(now.Add(-30*24*time.Hour), 100),
				entry(now.Add(-10*24*time.Hour), 105),
			},
		},
		{
			ExerciseID:   "b",
			ExerciseName: "B",
			Entries: []OneRepMaxEntry{
				entry(now.Add(-20*24*time.Hour), 100),
				entry(now.Add(-5*24*time.Hour), 100),
			},
		},
	}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	for i := 1; i < len(result.Points); i++ {
		if result.Points[i].PerformedAt.Before(result.Points[i-1].PerformedAt) {
			t.Errorf("points not sorted by performed_at ascending at index %d", i)
		}
	}
}

func TestComputeMuscleGroupProgression_EntriesOutsideWindowNotPlotted(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Narrow chart window — only the last 14 days.
	since := now.Add(-14 * 24 * time.Hour)
	until := now

	// Older entries (still within the 90-day baseline) should
	// contribute to the baseline but not as plotted points.
	histories := []ExerciseHistory{{
		ExerciseID:   "barbell-bench-press",
		ExerciseName: "Barbell Bench Press",
		Entries: []OneRepMaxEntry{
			entry(now.Add(-60*24*time.Hour), 200), // baseline only
			entry(now.Add(-30*24*time.Hour), 200), // baseline only
			entry(now.Add(-10*24*time.Hour), 200), // baseline + plot
			entry(now.Add(-3*24*time.Hour), 200),  // baseline + plot
		},
	}}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	if len(result.Points) != 2 {
		t.Errorf("expected only the 2 in-window entries as points, got %d", len(result.Points))
	}
}

func TestComputeMuscleGroupProgression_BaselinesSortedByName(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	histories := []ExerciseHistory{
		{ExerciseID: "z", ExerciseName: "Z Exercise",
			Entries: []OneRepMaxEntry{entry(now.Add(-5*24*time.Hour), 100)}},
		{ExerciseID: "a", ExerciseName: "A Exercise",
			Entries: []OneRepMaxEntry{entry(now.Add(-5*24*time.Hour), 100)}},
		{ExerciseID: "m", ExerciseName: "M Exercise",
			Entries: []OneRepMaxEntry{entry(now.Add(-5*24*time.Hour), 100)}},
	}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	want := []string{"A Exercise", "M Exercise", "Z Exercise"}
	if len(result.ExerciseBaselines) != len(want) {
		t.Fatalf("expected %d baselines, got %d", len(want), len(result.ExerciseBaselines))
	}
	for i, w := range want {
		if result.ExerciseBaselines[i].ExerciseName != w {
			t.Errorf("baseline[%d].name: got %q, want %q", i, result.ExerciseBaselines[i].ExerciseName, w)
		}
	}
}

func TestComputeMuscleGroupProgression_PerExerciseSlope(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	month := time.Duration(monthHours * float64(time.Hour))

	// Up-trending exercise: values increase linearly over time.
	// Down-trending exercise: values decrease linearly over time.
	up := []OneRepMaxEntry{
		entry(since.Add(0*month), 200),
		entry(since.Add(1*month), 210),
		entry(since.Add(2*month), 220),
		entry(since.Add(3*month-time.Hour), 230),
	}
	down := []OneRepMaxEntry{
		entry(since.Add(0*month), 230),
		entry(since.Add(1*month), 220),
		entry(since.Add(2*month), 210),
		entry(since.Add(3*month-time.Hour), 200),
	}
	for i := range up {
		up[i].ExerciseID = "up"
		up[i].WorkoutID = "wu" + time.Duration(i).String()
	}
	for i := range down {
		down[i].ExerciseID = "down"
		down[i].WorkoutID = "wd" + time.Duration(i).String()
	}
	histories := []ExerciseHistory{
		{ExerciseID: "up", ExerciseName: "Up", Entries: up},
		{ExerciseID: "down", ExerciseName: "Down", Entries: down},
	}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)

	// Independently recompute slope_per_month from the emitted points and
	// assert the reported slope matches (see analyticalSlopePerMonth).
	analytical := func(exID string) float64 {
		return analyticalSlopePerMonth(result, exID, since)
	}

	upTrend := trendForExercise(result, "up")
	downTrend := trendForExercise(result, "down")
	if upTrend == nil || downTrend == nil {
		t.Fatal("expected per-exercise trends for both exercises")
		return
	}
	if upTrend.SlopePerMonth == nil || downTrend.SlopePerMonth == nil {
		t.Fatal("expected slope for both exercises")
		return
	}
	if math.Abs(*upTrend.SlopePerMonth-analytical("up")) > 0.05 {
		t.Errorf("up slope: got %v, analytical %v", *upTrend.SlopePerMonth, analytical("up"))
	}
	if math.Abs(*downTrend.SlopePerMonth-analytical("down")) > 0.05 {
		t.Errorf("down slope: got %v, analytical %v", *downTrend.SlopePerMonth, analytical("down"))
	}
	if *upTrend.SlopePerMonth <= 0 {
		t.Errorf("expected up exercise to have positive slope, got %v", *upTrend.SlopePerMonth)
	}
	if *downTrend.SlopePerMonth >= 0 {
		t.Errorf("expected down exercise to have negative slope, got %v", *downTrend.SlopePerMonth)
	}
	if upTrend.Trendline == nil || downTrend.Trendline == nil {
		t.Error("expected non-nil trendlines on qualifying exercises")
	}

	// No top-level trendline field in the marshaled JSON.
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["trendline"]; ok {
		t.Error("expected no top-level trendline field")
	}
}

func TestComputeMuscleGroupProgression_MinSessionsThreshold(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	// Two in-window sessions: points + baseline emitted, but the trend
	// entry has null slope + trendline and the exercise is excluded from
	// lifts_tracked.
	histories := []ExerciseHistory{{
		ExerciseID:   "barbell-bench-press",
		ExerciseName: "Barbell Bench Press",
		Entries: []OneRepMaxEntry{
			entry(now.Add(-30*24*time.Hour), 200),
			entry(now.Add(-5*24*time.Hour), 210),
		},
	}}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	if len(result.Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(result.Points))
	}
	if len(result.ExerciseBaselines) != 1 {
		t.Fatalf("expected 1 baseline, got %d", len(result.ExerciseBaselines))
	}
	trend := trendForExercise(result, "barbell-bench-press")
	if trend == nil {
		t.Fatal("expected a per-exercise trend entry")
		return
	}
	if trend.SessionCount != 2 {
		t.Errorf("session_count: got %d, want 2", trend.SessionCount)
	}
	if trend.SlopePerMonth != nil {
		t.Errorf("expected null slope below threshold, got %v", *trend.SlopePerMonth)
	}
	if trend.Trendline != nil {
		t.Error("expected null trendline below threshold")
	}
	if result.Aggregate.LiftsTracked != 0 {
		t.Errorf("expected lifts_tracked 0 (below threshold), got %d", result.Aggregate.LiftsTracked)
	}
	if result.Aggregate.MedianSlopePerMonth != nil {
		t.Error("expected nil median when no exercise qualifies")
	}
}

// slopedHistory builds a history whose normalized series produces a
// target slope_per_month. We construct 5 monthly points; the baseline
// (recency-weighted, anchored at now) is dominated by the latest values,
// so to get a clean analytical slope we instead assert against the
// implementation's own emitted slope rather than the input. For the
// median/progressing aggregate tests we only need exercises whose slopes
// land at distinct, predictable signs/magnitudes, which a linear ramp in
// raw weight produces.
func slopedHistory(id string, since time.Time, perMonth float64) ExerciseHistory {
	month := time.Duration(monthHours * float64(time.Hour))
	base := 200.0
	var entries []OneRepMaxEntry
	for i := 0; i < 5; i++ {
		at := since.Add(time.Duration(i) * month)
		if i == 4 {
			at = at.Add(-time.Hour)
		}
		e := entry(at, base+perMonth*float64(i))
		e.ExerciseID = id
		e.WorkoutID = id + "-" + time.Duration(i).String()
		entries = append(entries, e)
	}
	return ExerciseHistory{ExerciseID: id, ExerciseName: id, Entries: entries}
}

func TestComputeMuscleGroupProgression_Aggregate_MedianSlope(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	// Five exercises with raw monthly deltas chosen so their emitted
	// slope_per_month values are [-1.0, 0.5, 1.0, 2.0, 3.0] → median 1.0.
	// We discover the per-month raw delta empirically by scaling: the
	// relationship between raw delta and slope_per_month is linear, so we
	// first probe with delta=1 then scale each exercise.
	probe := ComputeMuscleGroupProgression(chestFilter,
		[]ExerciseHistory{slopedHistory("probe", since, 1.0)}, since, until, now)
	pt := trendForExercise(probe, "probe")
	if pt == nil || pt.SlopePerMonth == nil {
		t.Fatal("probe produced no slope")
	}
	unit := *pt.SlopePerMonth // slope_per_month for a raw delta of 1.0/month

	want := []float64{-1.0, 0.5, 1.0, 2.0, 3.0}
	var histories []ExerciseHistory
	for i, target := range want {
		delta := target / unit
		histories = append(histories, slopedHistory("ex"+time.Duration(i).String(), since, delta))
	}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)

	// Independent anchor: each emitted slope must match the analytical fit of
	// its own emitted points, so a wrong production scaling/formula fails here
	// even though the inputs were sized via the probe above.
	for i := range want {
		exID := "ex" + time.Duration(i).String()
		trend := trendForExercise(result, exID)
		if trend == nil || trend.SlopePerMonth == nil {
			t.Fatalf("expected slope for %s", exID)
		}
		got := analyticalSlopePerMonth(result, exID, since)
		if math.Abs(*trend.SlopePerMonth-got) > 0.05 {
			t.Errorf("%s slope: emitted %v, analytical %v", exID, *trend.SlopePerMonth, got)
		}
	}

	if result.Aggregate.MedianSlopePerMonth == nil {
		t.Fatal("expected non-nil median")
	}
	if math.Abs(*result.Aggregate.MedianSlopePerMonth-1.0) > 0.05 {
		t.Errorf("median_slope_per_month: got %v, want ~1.0", *result.Aggregate.MedianSlopePerMonth)
	}
}

func TestComputeMuscleGroupProgression_Aggregate_LiftsProgressing(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	probe := ComputeMuscleGroupProgression(chestFilter,
		[]ExerciseHistory{slopedHistory("probe", since, 1.0)}, since, until, now)
	pt := trendForExercise(probe, "probe")
	if pt == nil || pt.SlopePerMonth == nil {
		t.Fatal("probe produced no slope")
	}
	unit := *pt.SlopePerMonth

	// Slopes [-1.0, 0.5, 1.0, 2.0, 3.0] → 4 of them exceed +0.25.
	want := []float64{-1.0, 0.5, 1.0, 2.0, 3.0}
	var histories []ExerciseHistory
	for i, target := range want {
		delta := target / unit
		histories = append(histories, slopedHistory("ex"+time.Duration(i).String(), since, delta))
	}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)

	// Independent anchor: emitted slopes must match the analytical fit of their
	// own points, so the >0.25 progressing count rests on a real slope, not the
	// probe-derived input alone.
	for i := range want {
		exID := "ex" + time.Duration(i).String()
		trend := trendForExercise(result, exID)
		if trend == nil || trend.SlopePerMonth == nil {
			t.Fatalf("expected slope for %s", exID)
		}
		got := analyticalSlopePerMonth(result, exID, since)
		if math.Abs(*trend.SlopePerMonth-got) > 0.05 {
			t.Errorf("%s slope: emitted %v, analytical %v", exID, *trend.SlopePerMonth, got)
		}
	}

	if result.Aggregate.LiftsProgressing != 4 {
		t.Errorf("lifts_progressing: got %d, want 4", result.Aggregate.LiftsProgressing)
	}
	if result.Aggregate.LiftsTracked != 5 {
		t.Errorf("lifts_tracked: got %d, want 5", result.Aggregate.LiftsTracked)
	}
}

func TestComputeMuscleGroupProgression_TopLevelTrendlineRemoved(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)
	until := now

	histories := []ExerciseHistory{{
		ExerciseID:   "barbell-bench-press",
		ExerciseName: "Barbell Bench Press",
		Entries: []OneRepMaxEntry{
			entry(now.Add(-60*24*time.Hour), 200),
			entry(now.Add(-40*24*time.Hour), 210),
			entry(now.Add(-20*24*time.Hour), 220),
			entry(now.Add(-5*24*time.Hour), 230),
		},
	}}

	result := ComputeMuscleGroupProgression(chestFilter, histories, since, until, now)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["trendline"]; ok {
		t.Error("expected no top-level trendline field in marshaled response")
	}
	if _, ok := m["per_exercise_trends"]; !ok {
		t.Error("expected per_exercise_trends field present")
	}
	if _, ok := m["aggregate"]; !ok {
		t.Error("expected aggregate field present")
	}
}
