package recoverytrend

import (
	"fmt"
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

// wide builds a date-ascending window of len(hrv) days named d000, d001, …, so
// a test can assert on the exact dates ComputeSeries echoes back. Resting HR
// and recovery score are left nil — ComputeSeries only reads HRV.
func wide(hrv []*float64) []Day {
	days := make([]Day, len(hrv))
	for i, v := range hrv {
		days[i] = Day{Date: fmt.Sprintf("d%03d", i), HRV: v}
	}
	return days
}

// ramp returns n HRV pointers rising linearly from start by step per day.
func ramp(n int, start, step float64) []*float64 {
	out := make([]*float64, n)
	for i := range out {
		out[i] = p(start + step*float64(i))
	}
	return out
}

func TestComputeSeries_LengthAndLeadIn(t *testing.T) {
	e := New(defaultCfg())
	days := wide(flat(61, 80))
	series, _ := e.ComputeSeries(days)
	if len(series) != 31 {
		t.Fatalf("len(series) = %d, want 31", len(series))
	}
	for i, r := range series {
		if want := days[30+i].Date; r.Date != want {
			t.Errorf("series[%d].Date = %q, want %q", i, r.Date, want)
		}
	}
}

func TestComputeSeries_PerDayBaselineExcludesTheDayItself(t *testing.T) {
	e := New(defaultCfg())
	hrv := flat(61, 80)
	// One large outlier at charted index 10 (input index 40).
	hrv[40] = p(200)
	series, _ := e.ComputeSeries(wide(hrv))

	// The outlier's OWN baseline is the 30 flat days behind it — untouched.
	if !approx(series[10].BaselineAvg, 80) {
		t.Errorf("outlier day's own baseline = %v, want 80 (day excluded from its own sample)", series[10].BaselineAvg)
	}
	// The NEXT day's baseline includes it: (29*80 + 200)/30 = 84.
	if !approx(series[11].BaselineAvg, 84) {
		t.Errorf("next day's baseline = %v, want 84 (outlier now in sample)", series[11].BaselineAvg)
	}
}

func TestComputeSeries_AgreesWithComputeOnToday(t *testing.T) {
	// Run the same agreement block under two BalancedZ values. The shipped 1.0
	// is a multiplicative identity, so a band that dropped the BalancedZ factor
	// altogether would still agree with itself; 1.5 makes the factor
	// observable. Equality is exact (!=, not approx) — the two entry points
	// share band/classify, so agreement to the last bit is the whole invariant.
	widerZ := defaultCfg()
	widerZ.BalancedZ = 1.5
	cfgs := []struct {
		name string
		cfg  Config
	}{
		{"balanced_z_1.0", defaultCfg()},
		{"balanced_z_1.5", widerZ},
	}
	for _, tc := range cfgs {
		t.Run(tc.name, func(t *testing.T) {
			e := New(tc.cfg)
			days := wide(ramp(61, 70, 0.5))
			series, _ := e.ComputeSeries(days)
			baseline, hrv := e.Compute(days[30:])

			last := series[len(series)-1]
			if last.BaselineAvg == nil || baseline.HRVAvg == nil || *last.BaselineAvg != *baseline.HRVAvg {
				t.Errorf("BaselineAvg = %v, want scalar HRVAvg %v (exact)", last.BaselineAvg, baseline.HRVAvg)
			}
			if last.BalancedLow == nil || hrv.BalancedLow == nil || *last.BalancedLow != *hrv.BalancedLow {
				t.Errorf("BalancedLow = %v, want %v (exact)", last.BalancedLow, hrv.BalancedLow)
			}
			if last.BalancedHigh == nil || hrv.BalancedHigh == nil || *last.BalancedHigh != *hrv.BalancedHigh {
				t.Errorf("BalancedHigh = %v, want %v (exact)", last.BalancedHigh, hrv.BalancedHigh)
			}
			if last.ZScore == nil || hrv.ZScore == nil || *last.ZScore != *hrv.ZScore {
				t.Errorf("ZScore = %v, want %v (exact)", last.ZScore, hrv.ZScore)
			}
			if last.Status != hrv.Status {
				t.Errorf("Status = %q, want %q", last.Status, hrv.Status)
			}
		})
	}
}

func TestComputeSeries_ShortHistoryLeavesEarlyDaysUnknown(t *testing.T) {
	e := New(defaultCfg())
	hrv := make([]*float64, 61)
	// Only the last 25 input days have readings, so the first charted days have
	// fewer than min_baseline_days (14) behind them.
	for i := 36; i < 61; i++ {
		hrv[i] = p(80)
	}
	series, _ := e.ComputeSeries(wide(hrv))

	// Charted index 0 is input index 30: its sample is days[0:30] — all nil.
	if series[0].BaselineAvg != nil || series[0].BalancedLow != nil || series[0].BalancedHigh != nil {
		t.Errorf("series[0] should carry no band, got %+v", series[0])
	}
	if series[0].Status != StatusUnknown {
		t.Errorf("series[0].Status = %q, want %q", series[0].Status, StatusUnknown)
	}
	// Charted index 20 is input index 50: its sample days[20:50] holds 14
	// readings (indices 36..49) — exactly min_baseline_days.
	if !approx(series[20].BaselineAvg, 80) {
		t.Errorf("series[20].BaselineAvg = %v, want 80", series[20].BaselineAvg)
	}
	if series[20].Status != StatusBalanced {
		t.Errorf("series[20].Status = %q, want %q", series[20].Status, StatusBalanced)
	}
}

func TestComputeSeries_NullDayKeepsBandDropsZ(t *testing.T) {
	e := New(defaultCfg())
	hrv := flat(61, 80)
	hrv[45] = nil // charted index 15, a missing morning with a full history
	series, _ := e.ComputeSeries(wide(hrv))

	r := series[15]
	if !approx(r.BaselineAvg, 80) {
		t.Errorf("BaselineAvg = %v, want 80 — a missing morning must not erase the band", r.BaselineAvg)
	}
	if r.BalancedLow == nil || r.BalancedHigh == nil {
		t.Errorf("bounds should survive a null reading, got %+v", r)
	}
	if r.ZScore != nil {
		t.Errorf("ZScore = %v, want nil", r.ZScore)
	}
	if r.Status != StatusUnknown {
		t.Errorf("Status = %q, want %q", r.Status, StatusUnknown)
	}
}

func TestDrift_RisingFallingSteady(t *testing.T) {
	e := New(defaultCfg())
	// Drift reaches back BaselineDriftDays(28) from the newest charted day:
	// back = len(series)-1-28 = 2, so it compares charted 30 (input 60) against
	// charted 2 (input 32). Delta and from-value are pinned exactly, not merely
	// non-nil — otherwise an off-by-one in `back`, or reporting `now` as `from`,
	// reads identically on a linear ramp where every 28-day step is the same.
	//   rising:  baseline[30] = mean(85..99.5) = 92.25, baseline[2] = mean(71..85.5) = 78.25
	//   falling: baseline[30] = mean(85..70.5) = 77.75, baseline[2] = mean(99..84.5) = 91.75
	cases := []struct {
		name      string
		hrv       []*float64
		want      string
		wantDelta float64
		wantFrom  float64
	}{
		{"rising", ramp(61, 70, 0.5), TrendRising, 14, 78.25},
		{"falling", ramp(61, 100, -0.5), TrendFalling, -14, 91.75},
		{"steady", flat(61, 80), TrendSteady, 0, 80},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, bt := e.ComputeSeries(wide(tc.hrv))
			if bt.Direction != tc.want {
				t.Errorf("Direction = %q, want %q", bt.Direction, tc.want)
			}
			// approx subsumes the nil check: it returns false on a nil pointer.
			if !approx(bt.DeltaMs, tc.wantDelta) {
				t.Errorf("DeltaMs = %v, want %v (now − then, exact)", bt.DeltaMs, tc.wantDelta)
			}
			if !approx(bt.FromAvg, tc.wantFrom) {
				t.Errorf("FromAvg = %v, want %v (the baseline 28 days back, not today's)", bt.FromAvg, tc.wantFrom)
			}
			if bt.OverDays != 28 {
				t.Errorf("OverDays = %d, want 28", bt.OverDays)
			}
		})
	}
}

func TestDrift_ThresholdIsSDRelative(t *testing.T) {
	e := New(defaultCfg())
	// A pure linear ramp can NEVER read steady: slope drives both the move and
	// the spread, so delta/threshold pins at ~9x no matter how gentle the
	// slope. Nothing in a ramp-only suite constrains BaselineDriftZ — drop the
	// factor to 0 and the ramp cases stay green. Constraining it needs a wide
	// spread under a SMALL move, which is what the oscillation below buys.
	//
	// HRV is level(i) + osc(i), where osc alternates ±20 and level steps by d
	// at i=32. The oscillation sums to exactly zero over ANY 30 consecutive
	// indices, so it inflates every SD without shifting a single baseline —
	// delete it as "noise" and both thresholds below collapse. Hence:
	//   baseline[2]  = mean(days[2:32])  = 80 exactly (level flat, osc cancels)
	//   baseline[30] = mean(days[30:60]) = 80 + d*28/30 (28 of the 30 stepped)
	//
	// The ±20 MAGNITUDE is load-bearing too: it puts sdEff at ~20, so the
	// threshold lands at 0.35*20 ~= 7, which is what brackets the two moves
	// below (3.73 and 14.93) with ~2x clearance on either side. Shrink it and
	// the steady case crosses the threshold; grow it and the rising case stops
	// crossing. The fixture is symmetric about the level, so the spread — and
	// therefore the threshold — is identical for negative d.
	step := func(d float64) []*float64 {
		out := make([]*float64, 61)
		for i := range out {
			level := 80.0
			// 32 is the EXCLUSIVE end of the `then` window days[2:32], so every
			// day in that sample predates the step — which is what pins FromAvg
			// at exactly 80 while the newest baseline carries d*28/30 of it.
			if i >= 32 {
				level += d
			}
			osc := 20.0
			if i%2 == 1 {
				osc = -20
			}
			out[i] = p(level + osc)
		}
		return out
	}

	cases := []struct {
		name string
		d    float64
		want string
	}{
		// delta = 4*28/30 = 3.733; sdEff = 20.025; threshold = 0.35*20.025 = 7.009.
		// A real, nonzero baseline move that is still smaller than the noise.
		{"below_threshold_reads_steady", 4, TrendSteady},
		// delta = 16*28/30 = 14.933; sdEff = 20.394; threshold = 7.138.
		{"above_threshold_reads_rising", 16, TrendRising},
		// Both arms of the switch need their own threshold case: the linear
		// `falling` ramp cannot constrain the negative side any more than the
		// linear `rising` ramp could constrain the positive one. Deltas mirror
		// exactly (-3.733, -14.933) against the same ~7 threshold.
		{"below_threshold_negative_reads_steady", -4, TrendSteady},
		{"above_threshold_negative_reads_falling", -16, TrendFalling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, bt := e.ComputeSeries(wide(step(tc.d)))
			if bt.Direction != tc.want {
				t.Errorf("Direction = %q, want %q (delta %.3f vs threshold +/-~7)", bt.Direction, tc.want, tc.d*28/30)
			}
			if wantDelta := tc.d * 28 / 30; !approx(bt.DeltaMs, wantDelta) {
				t.Errorf("DeltaMs = %v, want %v", bt.DeltaMs, wantDelta)
			}
			if !approx(bt.FromAvg, 80) {
				t.Errorf("FromAvg = %v, want 80 (oscillation cancels over the 30-day sample)", bt.FromAvg)
			}
		})
	}
}

func TestDrift_UnknownWhenSeriesTooShort(t *testing.T) {
	cfg := defaultCfg()
	cfg.BaselineDriftDays = 40 // the documented misconfiguration: > the series
	e := New(cfg)
	_, bt := e.ComputeSeries(wide(flat(61, 80)))
	if bt.Direction != TrendUnknown {
		t.Errorf("Direction = %q, want %q", bt.Direction, TrendUnknown)
	}
	if bt.DeltaMs != nil || bt.FromAvg != nil {
		t.Errorf("DeltaMs/FromAvg = %v/%v, want nil/nil", bt.DeltaMs, bt.FromAvg)
	}
	if bt.OverDays != 40 {
		t.Errorf("OverDays = %d, want 40", bt.OverDays)
	}
}

func TestDrift_UnknownWhenBaselineAbsent(t *testing.T) {
	e := New(defaultCfg())
	series, bt := e.ComputeSeries(wide(make([]*float64, 61)))
	if len(series) != 31 {
		t.Fatalf("len(series) = %d, want 31", len(series))
	}
	if bt.Direction != TrendUnknown {
		t.Errorf("Direction = %q, want %q", bt.Direction, TrendUnknown)
	}
	if bt.DeltaMs != nil || bt.FromAvg != nil {
		t.Errorf("DeltaMs/FromAvg = %v/%v, want nil/nil", bt.DeltaMs, bt.FromAvg)
	}
	if bt.OverDays != 28 {
		t.Errorf("OverDays = %d, want 28", bt.OverDays)
	}
}

func TestDrift_IsNotShortAvg(t *testing.T) {
	e := New(defaultCfg())
	// Baseline climbing all month (0..53 rise 70→96.5), then the last week
	// collapses to 75. The 30-day baseline is still well above where it stood
	// four weeks ago, while the 7-day mean sits well below it.
	hrv := ramp(61, 70, 0.5)
	for i := 54; i < 61; i++ {
		hrv[i] = p(75)
	}
	days := wide(hrv)

	_, hrvBlock := e.Compute(days[30:])
	_, bt := e.ComputeSeries(days)

	if hrvBlock.Trend != TrendFalling {
		t.Errorf("HRV.Trend = %q, want %q (7-day mean below baseline)", hrvBlock.Trend, TrendFalling)
	}
	if bt.Direction != TrendRising {
		t.Errorf("BaselineTrend.Direction = %q, want %q (baseline above its own past)", bt.Direction, TrendRising)
	}
}
