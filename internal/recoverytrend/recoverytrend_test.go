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
		BaselineDriftDays:  28,
		BaselineDriftZ:     0.35,
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

// window builds a Day slice of length baseline+1 (baseline sample + today).
// hrv[i] applies to sample day i; todayHRV is today's HRV (nil-able via ptr).
// Resting HR and recovery score are set uniformly so only HRV drives the math
// under test, except where a test overrides them.
func window(sampleHRV []*float64, todayHRV *float64) []Day {
	days := make([]Day, 0, len(sampleHRV)+1)
	for i, v := range sampleHRV {
		days = append(days, Day{Date: "s", HRV: v, RestingHR: p(float64(50 + i)), RecoveryScore: p(60)})
	}
	days = append(days, Day{Date: "today", HRV: todayHRV, RestingHR: p(52), RecoveryScore: p(58)})
	return days
}

// flat returns n identical HRV pointers of value v.
func flat(n int, v float64) []*float64 {
	out := make([]*float64, n)
	for i := range out {
		out[i] = p(v)
	}
	return out
}

func TestCompute_BaselineAveragesExcludeToday(t *testing.T) {
	e := New(defaultCfg())
	// 14 baseline HRV samples all 80, today 100. Baseline avg must be 80 (today
	// excluded), not (80*14+100)/15.
	b, _ := e.Compute(window(flat(14, 80), p(100)))
	if !approx(b.HRVAvg, 80) {
		t.Errorf("HRVAvg = %v, want 80 (today excluded)", b.HRVAvg)
	}
	if b.HRVDays != 14 {
		t.Errorf("HRVDays = %d, want 14", b.HRVDays)
	}
	if !approx(b.HRVStdDev, 0) {
		t.Errorf("HRVStdDev = %v, want 0", b.HRVStdDev)
	}
}

func TestCompute_BelowMinBaselineEmitsNullAvgWithCount(t *testing.T) {
	e := New(defaultCfg())
	// 13 samples < MinBaselineDays(14): null avg, count still reported.
	b, h := e.Compute(window(flat(13, 80), p(90)))
	if b.HRVAvg != nil {
		t.Errorf("HRVAvg = %v, want nil below MinBaselineDays", b.HRVAvg)
	}
	if b.HRVDays != 13 {
		t.Errorf("HRVDays = %d, want 13", b.HRVDays)
	}
	if h.Status != StatusUnknown {
		t.Errorf("status = %q, want unknown (no baseline)", h.Status)
	}
	if h.BalancedLow != nil || h.ZScore != nil {
		t.Errorf("no band/z without a baseline, got %+v", h)
	}
	if h.Trend != TrendUnknown {
		t.Errorf("trend = %q, want unknown (no baseline)", h.Trend)
	}
}

func TestCompute_ExactlyMinBaselineEmitsAvg(t *testing.T) {
	e := New(defaultCfg())
	b, _ := e.Compute(window(flat(14, 80), p(80)))
	if !approx(b.HRVAvg, 80) {
		t.Errorf("HRVAvg = %v, want 80 at exactly MinBaselineDays", b.HRVAvg)
	}
}

func TestCompute_BandAndZUseFlooredSD(t *testing.T) {
	e := New(defaultCfg())
	// Zero-variance history → raw SD 0 → floored to MinStdDevMs(1.0). Today 82
	// vs baseline 80 → z = (82-80)/1 = 2.
	b, h := e.Compute(window(flat(14, 80), p(82)))
	if !approx(b.HRVStdDev, 0) {
		t.Errorf("raw HRVStdDev = %v, want 0", b.HRVStdDev)
	}
	if !approx(h.BalancedLow, 79) || !approx(h.BalancedHigh, 81) {
		t.Errorf("band = [%v,%v], want [79,81] (floored SD)", h.BalancedLow, h.BalancedHigh)
	}
	if !approx(h.ZScore, 2) {
		t.Errorf("z = %v, want 2", h.ZScore)
	}
	if h.Status != StatusElevated {
		t.Errorf("status = %q, want elevated", h.Status)
	}
}

func TestCompute_StatusBoundaryIsInclusiveBalanced(t *testing.T) {
	e := New(defaultCfg())
	// Floored SD 1.0, BalancedZ 1.0. Today 81 → z = 1.0 exactly → balanced.
	_, h := e.Compute(window(flat(14, 80), p(81)))
	if !approx(h.ZScore, 1) {
		t.Fatalf("z = %v, want 1", h.ZScore)
	}
	if h.Status != StatusBalanced {
		t.Errorf("status at |z|==BalancedZ = %q, want balanced (inclusive)", h.Status)
	}
}

func TestCompute_Suppressed(t *testing.T) {
	e := New(defaultCfg())
	_, h := e.Compute(window(flat(14, 80), p(78))) // z = -2
	if h.Status != StatusSuppressed {
		t.Errorf("status = %q, want suppressed", h.Status)
	}
}

func TestCompute_TodayNullYieldsUnknownStatusButKeepsBaseline(t *testing.T) {
	e := New(defaultCfg())
	b, h := e.Compute(window(flat(14, 80), nil))
	if !approx(b.HRVAvg, 80) {
		t.Errorf("HRVAvg = %v, want 80 (baseline still computes)", b.HRVAvg)
	}
	if h.BalancedLow == nil || h.BalancedHigh == nil {
		t.Errorf("band should still be emitted, got %+v", h)
	}
	if h.Status != StatusUnknown {
		t.Errorf("status = %q, want unknown when today null", h.Status)
	}
	if h.ZScore != nil {
		t.Errorf("z should be nil when today null, got %v", h.ZScore)
	}
}

func TestCompute_AllNullHRV(t *testing.T) {
	e := New(defaultCfg())
	nils := make([]*float64, 14)
	b, h := e.Compute(window(nils, nil))
	if b.HRVAvg != nil || b.HRVDays != 0 {
		t.Errorf("all-null HRV: avg=%v days=%d, want nil/0", b.HRVAvg, b.HRVDays)
	}
	if h.Status != StatusUnknown || h.Trend != TrendUnknown {
		t.Errorf("status/trend = %q/%q, want unknown/unknown", h.Status, h.Trend)
	}
}

func TestCompute_TrendRisingFallingSteady(t *testing.T) {
	e := New(defaultCfg())
	// The trend window OVERLAPS the baseline by design (the recent days sit
	// inside the baseline sample), so each case is built as an independent,
	// fully-explicit 20-day sample + today whose numbers are hand-computed —
	// never by mutating a shared slice, which would silently perturb the
	// baseline it is measured against. short_avg is the mean of the non-null HRV
	// over the last TrendWindowDays(7) entries INCLUDING today.

	// Falling: 14 sample days at 88, the last 6 at 68, today 68.
	//   baseline mean = (14*88 + 6*68)/20 = 82; popSD = sqrt(1680/20) ≈ 9.165.
	//   trend window (6 tail + today) all 68 → short_avg 68; delta = 68-82 = -14
	//   < -TrendZ*sdEff = -0.5*9.165 ≈ -4.58 → falling.
	falling := make([]*float64, 0, 20)
	for i := 0; i < 14; i++ {
		falling = append(falling, p(88))
	}
	for i := 0; i < 6; i++ {
		falling = append(falling, p(68))
	}
	_, h := e.Compute(window(falling, p(68)))
	if !approx(h.ShortAvg, 68) {
		t.Errorf("falling short_avg = %v, want 68", h.ShortAvg)
	}
	if h.Trend != TrendFalling {
		t.Errorf("trend = %q, want falling", h.Trend)
	}

	// Rising: 14 sample days at 72, the last 6 at 92, today 92.
	//   baseline mean = (14*72 + 6*92)/20 = 78; popSD ≈ 9.165.
	//   trend window all 92 → delta = 92-78 = 14 > 4.58 → rising.
	rising := make([]*float64, 0, 20)
	for i := 0; i < 14; i++ {
		rising = append(rising, p(72))
	}
	for i := 0; i < 6; i++ {
		rising = append(rising, p(92))
	}
	_, h = e.Compute(window(rising, p(92)))
	if !approx(h.ShortAvg, 92) {
		t.Errorf("rising short_avg = %v, want 92", h.ShortAvg)
	}
	if h.Trend != TrendRising {
		t.Errorf("trend = %q, want rising", h.Trend)
	}

	// Steady: 20 sample days at 80, today 80. SD 0 → sdEff floored to 1.0;
	//   delta 0 → within ±0.5*1.0 → steady.
	_, h = e.Compute(window(flat(20, 80), p(80)))
	if !approx(h.ShortAvg, 80) {
		t.Errorf("steady short_avg = %v, want 80", h.ShortAvg)
	}
	if h.Trend != TrendSteady {
		t.Errorf("trend = %q, want steady", h.Trend)
	}
}

func TestCompute_TrendUnknownBelowMinTrendDays(t *testing.T) {
	e := New(defaultCfg())
	// Baseline is VALID (14 non-null HRV samples), but the trend window (last 7
	// entries: 6 sample tail + today) holds too few non-null values. Sample =
	// 14 days at 80 then 6 null days; today 80. The trend tail is 6 nulls +
	// today → 1 non-null < MinTrendDays(4) → trend unknown, even though the
	// baseline itself computes.
	sample := make([]*float64, 0, 20)
	for i := 0; i < 14; i++ {
		sample = append(sample, p(80))
	}
	for i := 0; i < 6; i++ {
		sample = append(sample, nil)
	}
	b, h := e.Compute(window(sample, p(80)))
	if b.HRVAvg == nil {
		t.Fatalf("baseline should still compute (14 samples), got nil avg")
	}
	if h.Trend != TrendUnknown {
		t.Errorf("trend = %q, want unknown (< MinTrendDays)", h.Trend)
	}
}

func TestCompute_PartialNullsPerMetricCounts(t *testing.T) {
	e := New(defaultCfg())
	// 14 sample days: all have a recovery score, but only the first 10 have HRV.
	days := make([]Day, 0, 15)
	for i := 0; i < 14; i++ {
		var hrv *float64
		if i < 10 {
			hrv = p(80)
		}
		days = append(days, Day{Date: "s", HRV: hrv, RestingHR: p(50), RecoveryScore: p(60)})
	}
	days = append(days, Day{Date: "today", HRV: p(80), RestingHR: p(52), RecoveryScore: p(58)})
	b, _ := e.Compute(days)
	if b.RecoveryScoreDays != 14 || b.RestingHRDays != 14 {
		t.Errorf("score/rhr days = %d/%d, want 14/14", b.RecoveryScoreDays, b.RestingHRDays)
	}
	if b.HRVDays != 10 {
		t.Errorf("HRVDays = %d, want 10 (per-metric count)", b.HRVDays)
	}
	if b.HRVAvg != nil { // 10 < MinBaselineDays(14)
		t.Errorf("HRVAvg = %v, want nil (10 < 14)", b.HRVAvg)
	}
	if !approx(b.RecoveryScoreAvg, 60) {
		t.Errorf("RecoveryScoreAvg = %v, want 60", b.RecoveryScoreAvg)
	}
}
