package recoverytrend

import (
	"math"
	"testing"
)

// defaultCfg mirrors the committed [recovery] block so the unit tests exercise
// the shipped tunables.
func defaultCfg() Config {
	return Config{
		BaselineWindowDays: 30,
		MinBaselineDays:    14,
		TrendWindowDays:    7,
		MinTrendDays:       4,
		BalancedZ:          1.0,
		TrendZ:             0.5,
		MinStdDevMs:        1.0,
	}
}

func p(f float64) *float64 { return &f }

func approx(a *float64, want float64) bool {
	return a != nil && math.Abs(*a-want) < 1e-9
}

func TestCompute_EmptyWindow(t *testing.T) {
	e := New(defaultCfg())
	b, h := e.Compute(nil)
	if b.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30", b.WindowDays)
	}
	if b.HRVAvg != nil || b.RestingHRAvg != nil || b.RecoveryScoreAvg != nil {
		t.Errorf("averages should be nil on empty window, got %+v", b)
	}
	if b.HRVDays != 0 || b.RestingHRDays != 0 || b.RecoveryScoreDays != 0 {
		t.Errorf("counts should be zero on empty window, got %+v", b)
	}
	if h.Status != StatusUnknown || h.Trend != TrendUnknown {
		t.Errorf("status/trend = %q/%q, want unknown/unknown", h.Status, h.Trend)
	}
	if h.ZScore != nil || h.BalancedLow != nil || h.BalancedHigh != nil || h.ShortAvg != nil {
		t.Errorf("hrv pointers should be nil on empty window, got %+v", h)
	}
}
