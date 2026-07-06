package dashboard

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

func bwEntry(weight float64, at time.Time) bodyweight.Entry {
	return bodyweight.Entry{
		Weight:     weight,
		Unit:       user.WeightUnitPounds,
		MeasuredAt: at,
	}
}

func TestBuildBodyweight_EmptyReturnsNil(t *testing.T) {
	if got := buildBodyweight(nil, bodyweight.Goal{}); got != nil {
		t.Errorf("no entries should be nil, got %+v", got)
	}
}

func TestBuildBodyweight_CurrentIsNewestRegardlessOfOrder(t *testing.T) {
	d0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	// Out-of-order slice: current must be the max MeasuredAt, not entries[0].
	entries := []bodyweight.Entry{
		bwEntry(195, d0.AddDate(0, 0, 7)),
		bwEntry(200, d0),
		bwEntry(190, d0.AddDate(0, 0, 14)),
	}
	got := buildBodyweight(entries, bodyweight.Goal{})
	if got == nil {
		t.Fatal("expected section")
		return
	}
	if got.Current != 190 {
		t.Errorf("current = %v, want 190 (newest)", got.Current)
	}
	if got.Unit != "lb" {
		t.Errorf("unit = %q, want lb", got.Unit)
	}
	// Trend spark is oldest→newest.
	wantSpark := []float64{200, 195, 190}
	if !reflect.DeepEqual(got.TrendSpark, wantSpark) {
		t.Errorf("trend spark = %v, want %v", got.TrendSpark, wantSpark)
	}
}

func TestBuildBodyweight_RateLosingWeightNegative(t *testing.T) {
	d0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	// Perfect -1 lb/day → -7 lb/week.
	entries := []bodyweight.Entry{
		bwEntry(200, d0),
		bwEntry(199, d0.AddDate(0, 0, 1)),
		bwEntry(198, d0.AddDate(0, 0, 2)),
		bwEntry(197, d0.AddDate(0, 0, 3)),
	}
	got := buildBodyweight(entries, bodyweight.Goal{})
	if got.RatePerWeek == nil {
		t.Fatal("expected a rate")
	}
	if math.Abs(*got.RatePerWeek-(-7)) > 1e-9 {
		t.Errorf("rate = %v, want -7", *got.RatePerWeek)
	}
}

func TestBuildBodyweight_SingleEntryNilRate(t *testing.T) {
	d0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	got := buildBodyweight([]bodyweight.Entry{bwEntry(180, d0)}, bodyweight.Goal{})
	if got.RatePerWeek != nil {
		t.Errorf("single entry should give nil rate, got %v", *got.RatePerWeek)
	}
}

func TestBuildBodyweight_ZeroSpanNilRate(t *testing.T) {
	d0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	// Two entries at the same instant → zero span → undefined slope.
	got := buildBodyweight([]bodyweight.Entry{bwEntry(180, d0), bwEntry(182, d0)}, bodyweight.Goal{})
	if got.RatePerWeek != nil {
		t.Errorf("zero span should give nil rate, got %v", *got.RatePerWeek)
	}
}

func TestBuildBodyweight_Goal(t *testing.T) {
	d0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	entries := []bodyweight.Entry{bwEntry(180, d0)}

	// Unset goal.
	got := buildBodyweight(entries, bodyweight.Goal{})
	if got.Goal != nil {
		t.Errorf("unset goal should be nil, got %+v", got.Goal)
	}

	// Set goal.
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	g := bodyweight.Goal{Weight: 170, Unit: user.WeightUnitKilograms, CreatedAt: &ts}
	got = buildBodyweight(entries, g)
	if got.Goal == nil {
		t.Fatal("expected goal")
	}
	if got.Goal.Weight != 170 || got.Goal.Unit != "kg" {
		t.Errorf("goal = %+v, want {170 kg}", got.Goal)
	}
}

func TestBuildBodyweight_SparkDownsampled(t *testing.T) {
	d0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	var entries []bodyweight.Entry
	for i := 0; i < 20; i++ {
		entries = append(entries, bwEntry(float64(200-i), d0.AddDate(0, 0, i)))
	}
	got := buildBodyweight(entries, bodyweight.Goal{})
	if len(got.TrendSpark) > bwSparkMax {
		t.Errorf("spark len = %d, want <= %d", len(got.TrendSpark), bwSparkMax)
	}
	// Endpoints retained: oldest 200, newest 181.
	if got.TrendSpark[0] != 200 || got.TrendSpark[len(got.TrendSpark)-1] != 181 {
		t.Errorf("spark endpoints = %v / %v, want 200 / 181",
			got.TrendSpark[0], got.TrendSpark[len(got.TrendSpark)-1])
	}
}
