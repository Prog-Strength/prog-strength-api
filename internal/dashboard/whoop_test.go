package dashboard

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/recoverytrend"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
)

func rhrPtr(f float64) *float64 { return &f }

// testRecoveryEngine returns an engine on the shipped default tunables, so the
// window width buildWhoop materializes matches production.
func testRecoveryEngine() *recoverytrend.Engine {
	return recoverytrend.New(recoverytrend.Config{
		BaselineWindowDays: 30,
		MinBaselineDays:    14,
		TrendWindowDays:    7,
		MinTrendDays:       4,
		BalancedZ:          1.0,
		TrendZ:             0.5,
		MinStdDevMs:        1.0,
		BaselineDriftDays:  28,
		BaselineDriftZ:     0.35,
	})
}

// entriesBetween builds daily whoop entries for the local dates from fromOffset
// days before now down to toOffset days before now (inclusive), oldest first.
// hrvAt is called with each entry's own offset, so a fixture can vary HRV
// across the window; the other two metrics are constant and uninteresting.
func entriesBetween(now time.Time, loc *time.Location, fromOffset, toOffset int, hrvAt func(offset int) float64) []whooprecovery.Entry {
	local := now.In(loc)
	y, mo, d := local.Date()
	out := make([]whooprecovery.Entry, 0, fromOffset-toOffset+1)
	for i := fromOffset; i >= toOffset; i-- {
		day := time.Date(y, mo, d-i, 0, 0, 0, 0, loc)
		out = append(out, whooprecovery.Entry{
			Date:             day.Format("2006-01-02"),
			RestingHeartRate: rhrPtr(52),
			RecoveryScore:    rhrPtr(65),
			HRVRmssdMilli:    rhrPtr(hrvAt(i)),
		})
	}
	return out
}

// entriesEndingAt builds n consecutive daily whoop entries ending on the local
// date of now, oldest first, with a constant HRV.
func entriesEndingAt(now time.Time, loc *time.Location, n int, hrv float64) []whooprecovery.Entry {
	return entriesBetween(now, loc, n-1, 0, func(int) float64 { return hrv })
}

// rampHRV gives every offset a DISTINCT HRV, rising toward today. Fixtures that
// need to tell one charted day's band from another's use it: under a constant
// HRV every day's trailing baseline is identical, so a mis-zipped band would be
// indistinguishable from a correct one.
func rampHRV(offset int) float64 { return 100 - float64(offset) }

func TestBuildWhoop_TodayAndSpark(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	// Late local evening to exercise the local-day boundary: 2026-06-17 23:30
	// Denver is 2026-06-18 05:30 UTC, so a naive UTC "today" would be wrong.
	now := time.Date(2026, 6, 17, 23, 30, 0, 0, denver)

	// Window is the 7 days 06-11 .. 06-17 (oldest→newest). 06-13 has a null RHR
	// (must be skipped); 06-18 is out of window (ignored).
	entries := []whooprecovery.Entry{
		{Date: "2026-06-11", RestingHeartRate: rhrPtr(50)},
		{Date: "2026-06-13", RestingHeartRate: nil}, // null RHR → skipped
		{Date: "2026-06-15", RestingHeartRate: rhrPtr(52)},
		{Date: "2026-06-17", RestingHeartRate: rhrPtr(48), RecoveryScore: rhrPtr(80), HRVRmssdMilli: rhrPtr(42)}, // today
		{Date: "2026-06-18", RestingHeartRate: rhrPtr(99)},                                                       // future/out-of-window
	}

	got := buildWhoop(entries, testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}

	// Spark: only days with a non-null RHR, oldest→newest.
	wantSpark := []float64{50, 52, 48}
	if !reflect.DeepEqual(got.RestingHRSpark, wantSpark) {
		t.Errorf("spark = %v, want %v", got.RestingHRSpark, wantSpark)
	}

	if got.Today == nil {
		t.Fatal("expected today's row")
		return
	}
	if got.Today.Date != "2026-06-17" {
		t.Errorf("today.date = %q, want 2026-06-17", got.Today.Date)
	}
	if got.Today.RestingHeartRate == nil || *got.Today.RestingHeartRate != 48 {
		t.Errorf("today.resting_heart_rate = %v, want 48", got.Today.RestingHeartRate)
	}
	if got.Today.RecoveryScore == nil || *got.Today.RecoveryScore != 80 {
		t.Errorf("today.recovery_score = %v, want 80", got.Today.RecoveryScore)
	}
	if got.Today.HRVRmssdMilli == nil || *got.Today.HRVRmssdMilli != 42 {
		t.Errorf("today.hrv_rmssd_milli = %v, want 42", got.Today.HRVRmssdMilli)
	}
}

func TestBuildWhoop_NoTodayRowStillPresent(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	// A row exists in-window but not for today — section present, Today nil.
	entries := []whooprecovery.Entry{
		{Date: "2026-06-12", RestingHeartRate: rhrPtr(55)},
	}
	got := buildWhoop(entries, testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}
	if got.Today != nil {
		t.Errorf("today should be nil when no row for today, got %+v", got.Today)
	}
	if want := []float64{55}; !reflect.DeepEqual(got.RestingHRSpark, want) {
		t.Errorf("spark = %v, want %v", got.RestingHRSpark, want)
	}
}

func TestBuildWhoop_NoEntriesEmptySpark(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)

	got := buildWhoop(nil, testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user with no data still gets a section")
		return
	}
	if got.Today != nil {
		t.Errorf("today should be nil, got %+v", got.Today)
	}
	if len(got.RestingHRSpark) != 0 {
		t.Errorf("spark should be empty, got %v", got.RestingHRSpark)
	}
	// Must serialize as [] not null.
	if got.RestingHRSpark == nil {
		t.Error("spark should be non-nil empty slice")
	}
}

func TestBuildWhoop_DaysDateAlignedWithGaps(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	entries := []whooprecovery.Entry{
		{Date: "2026-07-31", RestingHeartRate: rhrPtr(51), RecoveryScore: rhrPtr(66), HRVRmssdMilli: rhrPtr(81)},
		{Date: "2026-08-01", RestingHeartRate: rhrPtr(51), RecoveryScore: rhrPtr(58), HRVRmssdMilli: rhrPtr(74)}, // today
		// 2026-07-30 deliberately absent → must appear with null metrics.
	}
	got := buildWhoop(entries, testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}

	// Days spans baseline_window_days + 1 = 31 entries, oldest→newest, last=today.
	if len(got.Days) != 31 {
		t.Fatalf("len(Days) = %d, want 31", len(got.Days))
	}
	if got.Days[0].Date != "2026-07-02" {
		t.Errorf("Days[0].date = %q, want 2026-07-02", got.Days[0].Date)
	}
	if got.Days[len(got.Days)-1].Date != "2026-08-01" {
		t.Errorf("Days[last].date = %q, want 2026-08-01 (today)", got.Days[len(got.Days)-1].Date)
	}
	// The absent 07-30 is present with null metrics (not omitted, not zeroed).
	var found bool
	for _, day := range got.Days {
		if day.Date == "2026-07-30" {
			found = true
			if day.RestingHeartRate != nil || day.RecoveryScore != nil || day.HRVRmssdMilli != nil {
				t.Errorf("missing day 07-30 should have null metrics, got %+v", day)
			}
		}
	}
	if !found {
		t.Error("missing day 2026-07-30 should be present in Days")
	}
	// A present day carries its metrics.
	last := got.Days[len(got.Days)-1]
	if last.HRVRmssdMilli == nil || *last.HRVRmssdMilli != 74 {
		t.Errorf("today HRV in Days = %v, want 74", last.HRVRmssdMilli)
	}
}

func TestBuildWhoop_SparkAndTodayUnchangedRegression(t *testing.T) {
	// Guards the compatibility promise: for the same input, today and
	// resting_hr_spark are byte-identical to their pre-change values.
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 23, 30, 0, 0, denver)
	entries := []whooprecovery.Entry{
		{Date: "2026-06-11", RestingHeartRate: rhrPtr(50)},
		{Date: "2026-06-13", RestingHeartRate: nil},
		{Date: "2026-06-15", RestingHeartRate: rhrPtr(52)},
		{Date: "2026-06-17", RestingHeartRate: rhrPtr(48), RecoveryScore: rhrPtr(80), HRVRmssdMilli: rhrPtr(42)},
	}
	got := buildWhoop(entries, testRecoveryEngine(), now, denver)
	if want := []float64{50, 52, 48}; !reflect.DeepEqual(got.RestingHRSpark, want) {
		t.Errorf("spark = %v, want %v (unchanged)", got.RestingHRSpark, want)
	}
	if got.Today == nil || got.Today.Date != "2026-06-17" || got.Today.HRVRmssdMilli == nil || *got.Today.HRVRmssdMilli != 42 {
		t.Errorf("today = %+v, want unchanged 06-17 row", got.Today)
	}
}

func TestBuildWhoop_BaselineComputedFromDays(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	// 14 baseline days (07-18..07-31) all HRV 80, plus today (08-01) HRV 80.
	var entries []whooprecovery.Entry
	for d := 18; d <= 31; d++ {
		entries = append(entries, whooprecovery.Entry{
			Date:             time.Date(2026, 7, d, 0, 0, 0, 0, denver).Format("2006-01-02"),
			RestingHeartRate: rhrPtr(52),
			RecoveryScore:    rhrPtr(60),
			HRVRmssdMilli:    rhrPtr(80),
		})
	}
	entries = append(entries, whooprecovery.Entry{
		Date: "2026-08-01", RestingHeartRate: rhrPtr(52), RecoveryScore: rhrPtr(58), HRVRmssdMilli: rhrPtr(80),
	})

	got := buildWhoop(entries, testRecoveryEngine(), now, denver)
	if got.Baseline.HRVAvg == nil || *got.Baseline.HRVAvg != 80 {
		t.Errorf("Baseline.HRVAvg = %v, want 80", got.Baseline.HRVAvg)
	}
	if got.Baseline.HRVDays != 14 {
		t.Errorf("Baseline.HRVDays = %d, want 14", got.Baseline.HRVDays)
	}
	if got.Baseline.WindowDays != 30 {
		t.Errorf("Baseline.WindowDays = %d, want 30", got.Baseline.WindowDays)
	}
	if got.HRV.Status != "balanced" { // z = 0
		t.Errorf("HRV.Status = %q, want balanced", got.HRV.Status)
	}
}

func TestBuildWhoop_NoEntriesBaselineUnknown(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)
	got := buildWhoop(nil, testRecoveryEngine(), now, denver)
	if len(got.Days) != 31 {
		t.Errorf("len(Days) = %d, want 31 full null window", len(got.Days))
	}
	if got.Baseline.HRVAvg != nil || got.Baseline.HRVDays != 0 {
		t.Errorf("empty baseline: avg=%v days=%d, want nil/0", got.Baseline.HRVAvg, got.Baseline.HRVDays)
	}
	if got.HRV.Status != "unknown" || got.HRV.Trend != "unknown" {
		t.Errorf("status/trend = %q/%q, want unknown/unknown", got.HRV.Status, got.HRV.Trend)
	}
}

// TestBuildWhoop_ScalarBlocksUnchangedByLeadIn is the compatibility promise:
// adding a window of lead-in ahead of the charted days must not move the scalar
// baseline/hrv blocks by so much as a bit. The lead-in carries a wildly
// different HRV, so any leakage into Compute's sample would be obvious.
func TestBuildWhoop_ScalarBlocksUnchangedByLeadIn(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	narrow := entriesEndingAt(now, denver, 31, 80)
	before := buildWhoop(narrow, testRecoveryEngine(), now, denver)
	if before == nil {
		t.Fatal("connected user should always get a section")
		return
	}
	// Snapshot the values before the second call. buildWhoop allocates a fresh
	// section per call, so these are not aliased to the second result — the
	// assertions below would be vacuous if they were.
	wantBaseline, wantHRV := before.Baseline, before.HRV

	// 30 strictly-older dates (offsets 60..31) with a very different HRV, then
	// the same charted window.
	wide := append(entriesBetween(now, denver, 60, 31, func(int) float64 { return 200 }), narrow...)
	if len(wide) != 61 {
		t.Fatalf("len(wide) = %d, want 61", len(wide))
	}

	after := buildWhoop(wide, testRecoveryEngine(), now, denver)
	if after == nil {
		t.Fatal("connected user should always get a section")
		return
	}
	if !reflect.DeepEqual(after.Baseline, wantBaseline) {
		t.Errorf("Baseline changed by lead-in:\n got %+v\nwant %+v", after.Baseline, wantBaseline)
	}
	if !reflect.DeepEqual(after.HRV, wantHRV) {
		t.Errorf("HRV changed by lead-in:\n got %+v\nwant %+v", after.HRV, wantHRV)
	}
}

// TestBuildWhoop_DaysCarryPerDayBands pins the band-to-day correspondence end
// to end: EVERY charted day's date and its own trailing baseline, the baseline
// recomputed independently in the test rather than read back from the engine.
// The fixture ramps HRV so no two days share a baseline — under a constant HRV
// all 31 results are byte-identical and any mis-zip (an off-by-one, or a full
// reversal of the mapping) sails through. The last-day-agrees-with-the-scalar-
// block assertions follow; they are the right invariant but not sufficient
// alone.
func TestBuildWhoop_DaysCarryPerDayBands(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	got := buildWhoop(entriesBetween(now, denver, 60, 0, rampHRV), testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}
	if len(got.Days) != 31 {
		t.Fatalf("len(Days) = %d, want 31 charted days", len(got.Days))
	}

	for i, day := range got.Days {
		// Days[i] is the local date i-from-the-oldest, i.e. today − (30 − i).
		offset := 30 - i
		wantDate := time.Date(2026, 8, 1-offset, 0, 0, 0, 0, denver).Format("2006-01-02")
		if day.Date != wantDate {
			t.Errorf("Days[%d].date = %q, want %q", i, day.Date, wantDate)
		}
		// That day's band comes from the 30 dates PRECEDING it — offsets
		// offset+30 (oldest) down to offset+1 — summed oldest→newest, the same
		// order the engine collects them in.
		var sum float64
		for o := offset + 30; o >= offset+1; o-- {
			sum += rampHRV(o)
		}
		wantAvg := sum / 30
		if day.BaselineAvg == nil {
			t.Errorf("Days[%d] (%s) baseline_avg = nil, want %v", i, day.Date, wantAvg)
			continue
		}
		if math.Abs(*day.BaselineAvg-wantAvg) > 1e-9 {
			t.Errorf("Days[%d] (%s) baseline_avg = %v, want %v — band zipped to the wrong day",
				i, day.Date, *day.BaselineAvg, wantAvg)
		}
	}

	last := got.Days[len(got.Days)-1]
	if last.Date != "2026-08-01" {
		t.Errorf("Days[last].date = %q, want 2026-08-01 (today)", last.Date)
	}
	if last.Status != got.HRV.Status {
		t.Errorf("Days[last].status = %q, want %q (= hrv.status)", last.Status, got.HRV.Status)
	}
	if last.BalancedLow == nil || got.HRV.BalancedLow == nil || *last.BalancedLow != *got.HRV.BalancedLow {
		t.Errorf("Days[last].balanced_low = %v, want %v (= hrv.balanced_low)", last.BalancedLow, got.HRV.BalancedLow)
	}
	if last.BalancedHigh == nil || got.HRV.BalancedHigh == nil || *last.BalancedHigh != *got.HRV.BalancedHigh {
		t.Errorf("Days[last].balanced_high = %v, want %v (= hrv.balanced_high)", last.BalancedHigh, got.HRV.BalancedHigh)
	}
	if last.BaselineAvg == nil || got.Baseline.HRVAvg == nil || *last.BaselineAvg != *got.Baseline.HRVAvg {
		t.Errorf("Days[last].baseline_avg = %v, want %v (= baseline.hrv_avg)", last.BaselineAvg, got.Baseline.HRVAvg)
	}
}

// TestBuildWhoop_LeadInNotSerialized: the lead-in feeds the engine and is never
// emitted — the charted window still starts exactly baseline_window_days back.
func TestBuildWhoop_LeadInNotSerialized(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	got := buildWhoop(entriesEndingAt(now, denver, 61, 80), testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}
	const oldest = "2026-07-02" // today − 30 days
	if len(got.Days) == 0 {
		t.Fatal("Days should be the full charted window, got none")
		return
	}
	if got.Days[0].Date != oldest {
		t.Fatalf("Days[0].date = %q, want %s", got.Days[0].Date, oldest)
	}
	// Dates are YYYY-MM-DD, so lexicographic < is chronological <.
	for _, day := range got.Days {
		if day.Date < oldest {
			t.Errorf("lead-in day %q leaked into Days (oldest charted is %s)", day.Date, oldest)
		}
	}
}

// TestBuildWhoop_BaselineTrendPresentWhenNoEntries: a connected-but-empty user
// gets "nothing to say" as an unknown direction, never a missing key.
func TestBuildWhoop_BaselineTrendPresentWhenNoEntries(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	got := buildWhoop(nil, testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user with no data still gets a section")
		return
	}
	if got.BaselineTrend.Direction != recoverytrend.TrendUnknown {
		t.Errorf("baseline_trend.direction = %q, want %q", got.BaselineTrend.Direction, recoverytrend.TrendUnknown)
	}
	if got.BaselineTrend.OverDays != 28 {
		t.Errorf("baseline_trend.over_days = %d, want 28", got.BaselineTrend.OverDays)
	}
	if got.BaselineTrend.DeltaMs != nil || got.BaselineTrend.FromAvg != nil {
		t.Errorf("baseline_trend delta/from = %v/%v, want nil/nil", got.BaselineTrend.DeltaMs, got.BaselineTrend.FromAvg)
	}
}

// TestRecoverySection_JSONKeys is the contract assertion: the recovery section
// serializes exactly the keys the web mirror (lib/api.ts DashboardRecovery)
// consumes. If a field name drifts, this and the web type disagree.
func TestRecoverySection_JSONKeys(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)
	got := buildWhoop(nil, testRecoveryEngine(), now, denver)

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"today", "resting_hr_spark", "days", "baseline", "hrv", "baseline_trend"} {
		if _, ok := m[k]; !ok {
			t.Errorf("recovery section missing key %q", k)
		}
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(m["baseline"], &base); err != nil {
		t.Fatalf("unmarshal baseline: %v", err)
	}
	for _, k := range []string{"window_days", "resting_hr_avg", "resting_hr_days", "hrv_avg", "hrv_std_dev", "hrv_days", "recovery_score_avg", "recovery_score_days"} {
		if _, ok := base[k]; !ok {
			t.Errorf("baseline missing key %q", k)
		}
	}
	var hrv map[string]json.RawMessage
	if err := json.Unmarshal(m["hrv"], &hrv); err != nil {
		t.Fatalf("unmarshal hrv: %v", err)
	}
	for _, k := range []string{"status", "balanced_low", "balanced_high", "z_score", "trend", "short_avg"} {
		if _, ok := hrv[k]; !ok {
			t.Errorf("hrv missing key %q", k)
		}
	}
	var drift map[string]json.RawMessage
	if err := json.Unmarshal(m["baseline_trend"], &drift); err != nil {
		t.Fatalf("unmarshal baseline_trend: %v", err)
	}
	assertExactKeys(t, "baseline_trend", drift, "direction", "delta_ms", "from_avg", "over_days")

	// days[] is the embedded RecoveryDay's four keys flattened plus the five
	// band keys — nine, no more. A stray field here (e.g. someone replacing the
	// embed with a named member) silently changes the wire shape.
	var days []map[string]json.RawMessage
	if err := json.Unmarshal(m["days"], &days); err != nil {
		t.Fatalf("unmarshal days: %v", err)
	}
	if len(days) == 0 {
		t.Fatal("days should be a full null window, got none")
	}
	for i, day := range days {
		assertExactKeys(t, "days["+strconv.Itoa(i)+"]", day,
			"date", "resting_heart_rate", "recovery_score", "hrv_rmssd_milli",
			"baseline_avg", "balanced_low", "balanced_high", "z_score", "status")
	}
}

// TestRecoverySection_TodayJSONKeysUnchanged pins today's wire shape: exactly
// the original four keys. buildWhoop(nil, …) leaves Today null, so this needs
// its own fixture with a row for today.
//
// What this covers is Today's TYPE: widening it to RecoveryDayPoint would leak
// the five band keys in here and fail. It does NOT cover the embed flattening —
// Today is a *RecoveryDay, so it never exercises the embed at all; tagging the
// embed is caught by the days[] loop in TestRecoverySection_JSONKeys.
func TestRecoverySection_TodayJSONKeysUnchanged(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)
	got := buildWhoop(entriesEndingAt(now, denver, 61, 80), testRecoveryEngine(), now, denver)
	if got == nil || got.Today == nil {
		t.Fatal("expected a section with today's row")
		return
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var today map[string]json.RawMessage
	if err := json.Unmarshal(m["today"], &today); err != nil {
		t.Fatalf("unmarshal today: %v", err)
	}
	if today == nil {
		t.Fatal("today should be a non-null object")
	}
	// today keeps the bare RecoveryDay shape — no band fields leak in.
	assertExactKeys(t, "today", today, "date", "resting_heart_rate", "recovery_score", "hrv_rmssd_milli")
}

// assertExactKeys fails unless obj's key set is exactly want — no missing keys
// and, just as importantly, no extra ones.
func assertExactKeys(t *testing.T, label string, obj map[string]json.RawMessage, want ...string) {
	t.Helper()
	wanted := make(map[string]bool, len(want))
	for _, k := range want {
		wanted[k] = true
		if _, ok := obj[k]; !ok {
			t.Errorf("%s missing key %q", label, k)
		}
	}
	for k := range obj {
		if !wanted[k] {
			t.Errorf("%s has unexpected key %q", label, k)
		}
	}
}

// chartedDates is the Days slice's dates, in order.
func chartedDates(days []RecoveryDayPoint) []string {
	out := make([]string, len(days))
	for i, d := range days {
		out[i] = d.Date
	}
	return out
}

// TestBuildRecoveryView_NarrowWindowCharted is the lead-in proof: a window
// narrower than the dashboard's charts exactly its own dates, and its FIRST
// charted day already carries a band — computed from the 30 dates behind it,
// which the builder materialized itself and never serialized.
func TestBuildRecoveryView_NarrowWindowCharted(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	// Offsets 60..0 from today, i.e. 2026-06-02..2026-08-01, each with its own
	// HRV so no two days share a baseline.
	entries := entriesBetween(now, denver, 60, 0, rampHRV)

	// 2026-07-20 is offset 12; 2026-07-25 is offset 7.
	got := buildRecoveryView(entries, testRecoveryEngine(), "2026-07-20", "2026-07-25", now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}

	want := []string{"2026-07-20", "2026-07-21", "2026-07-22", "2026-07-23", "2026-07-24", "2026-07-25"}
	if !reflect.DeepEqual(chartedDates(got.Days), want) {
		t.Fatalf("Days dates = %v, want %v", chartedDates(got.Days), want)
	}

	// The first charted day's band comes from offsets 42 (oldest) down to 13.
	var sum float64
	for o := 12 + 30; o >= 12+1; o-- {
		sum += rampHRV(o)
	}
	wantAvg := sum / 30
	first := got.Days[0]
	if first.BaselineAvg == nil || math.Abs(*first.BaselineAvg-wantAvg) > 1e-9 {
		t.Errorf("Days[0] (%s) baseline_avg = %v, want %v — the lead-in is missing or misaligned",
			first.Date, first.BaselineAvg, wantAvg)
	}
	if first.BalancedLow == nil || first.BalancedHigh == nil {
		t.Errorf("Days[0] bands = %v/%v, want both present", first.BalancedLow, first.BalancedHigh)
	}
	if first.Status == recoverytrend.StatusUnknown {
		t.Error("Days[0].status = unknown, want a classified day with a full lead-in behind it")
	}

	// Today and the spark are NOT window-relative: both still describe now.
	if got.Today == nil || got.Today.Date != "2026-08-01" {
		t.Errorf("today = %+v, want the 2026-08-01 row regardless of the charted window", got.Today)
	}
	if len(got.RestingHRSpark) != recoverySparkDays {
		t.Errorf("len(spark) = %d, want %d trailing days from now", len(got.RestingHRSpark), recoverySparkDays)
	}
}

// TestBuildRecoveryView_LeadInBeforeOldestRow: a window whose lead-in reaches
// back past the oldest stored morning still emits every charted date — the
// short sample costs the band, never the slot.
func TestBuildRecoveryView_LeadInBeforeOldestRow(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	// Only 10 stored mornings (2026-07-23..2026-08-01), so every charted day
	// sees at most 9 samples — under min_baseline_days of 14.
	got := buildRecoveryView(entriesEndingAt(now, denver, 10, 80), testRecoveryEngine(), "2026-07-25", "2026-08-01", now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}

	want := []string{
		"2026-07-25", "2026-07-26", "2026-07-27", "2026-07-28",
		"2026-07-29", "2026-07-30", "2026-07-31", "2026-08-01",
	}
	if !reflect.DeepEqual(chartedDates(got.Days), want) {
		t.Fatalf("Days dates = %v, want %v", chartedDates(got.Days), want)
	}
	for i, day := range got.Days {
		if day.Status != recoverytrend.StatusUnknown {
			t.Errorf("Days[%d] (%s) status = %q, want unknown on a short sample", i, day.Date, day.Status)
		}
		if day.BaselineAvg != nil || day.BalancedLow != nil || day.BalancedHigh != nil || day.ZScore != nil {
			t.Errorf("Days[%d] (%s) should have null band fields, got %+v", i, day.Date, day)
		}
	}
	if got.HRV.Status != recoverytrend.StatusUnknown {
		t.Errorf("hrv.status = %q, want unknown", got.HRV.Status)
	}
}

// TestBuildRecoveryView_MonthBoundaryAndDST walks windows across both a month
// end and a Denver DST transition, in both directions — the 23h spring-forward
// and the 25h fall-back, since only the latter can make an accumulated walk
// repeat a date. The charted dates are asserted literally; the first charted
// day's baseline pins the builder's own lead-in walk, whose 30 dates must be
// exactly the 30 stored ones, which start at a literal date of their own.
func TestBuildRecoveryView_MonthBoundaryAndDST(t *testing.T) {
	denver := mustLoad(t, "America/Denver")

	tests := []struct {
		name     string
		now      time.Time
		from, to string
		leadFrom string // oldest stored morning: from − 30 days
		want     []string
	}{
		{
			name: "spring forward 2026-03-08",
			now:  time.Date(2026, 3, 10, 9, 0, 0, 0, denver),
			from: "2026-02-26", to: "2026-03-10",
			leadFrom: "2026-01-27",
			want: []string{
				"2026-02-26", "2026-02-27", "2026-02-28",
				"2026-03-01", "2026-03-02", "2026-03-03", "2026-03-04", "2026-03-05",
				"2026-03-06", "2026-03-07", "2026-03-08", "2026-03-09", "2026-03-10",
			},
		},
		{
			name: "fall back 2026-11-01",
			now:  time.Date(2026, 11, 5, 9, 0, 0, 0, denver),
			from: "2026-10-25", to: "2026-11-05",
			leadFrom: "2026-09-25",
			want: []string{
				"2026-10-25", "2026-10-26", "2026-10-27", "2026-10-28", "2026-10-29",
				"2026-10-30", "2026-10-31",
				"2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The 30 stored mornings immediately before from, HRV 1..30
			// oldest→newest so their mean pins which dates were sampled.
			leadFrom, err := time.Parse("2006-01-02", tt.leadFrom)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.leadFrom, err)
			}
			entries := make([]whooprecovery.Entry, 0, 30)
			for i := range 30 {
				entries = append(entries, whooprecovery.Entry{
					Date:             leadFrom.AddDate(0, 0, i).Format("2006-01-02"),
					RestingHeartRate: rhrPtr(52),
					RecoveryScore:    rhrPtr(60),
					HRVRmssdMilli:    rhrPtr(float64(i + 1)),
				})
			}

			got := buildRecoveryView(entries, testRecoveryEngine(), tt.from, tt.to, tt.now, denver)
			if got == nil {
				t.Fatal("connected user should always get a section")
				return
			}
			if !reflect.DeepEqual(chartedDates(got.Days), tt.want) {
				t.Fatalf("Days dates = %v, want %v", chartedDates(got.Days), tt.want)
			}
			const wantAvg = 15.5 // mean(1..30)
			if got.Days[0].BaselineAvg == nil || math.Abs(*got.Days[0].BaselineAvg-wantAvg) > 1e-9 {
				t.Errorf("Days[0] baseline_avg = %v, want %v — the lead-in walk drifted", got.Days[0].BaselineAvg, wantAvg)
			}
		})
	}
}

// TestBuildRecoveryView_MidnightDSTZone: America/Havana springs forward AT
// midnight, so 2018-03-11 00:00 local does not exist and time.Date normalizes
// it BACKWARDS into 2018-03-10. A walk anchored on local midnights therefore
// emitted 2018-03-10 twice and dropped 2018-03-11 altogether. Expectations are
// literal date lists — deriving them with the builder's own idiom is exactly
// how this survived three DST tests.
func TestBuildRecoveryView_MidnightDSTZone(t *testing.T) {
	havana := mustLoad(t, "America/Havana")

	t.Run("dashboard window derived from now", func(t *testing.T) {
		now := time.Date(2018, 4, 10, 9, 0, 0, 0, havana)
		want := []string{
			"2018-03-11", "2018-03-12", "2018-03-13", "2018-03-14", "2018-03-15",
			"2018-03-16", "2018-03-17", "2018-03-18", "2018-03-19", "2018-03-20",
			"2018-03-21", "2018-03-22", "2018-03-23", "2018-03-24", "2018-03-25",
			"2018-03-26", "2018-03-27", "2018-03-28", "2018-03-29", "2018-03-30",
			"2018-03-31",
			"2018-04-01", "2018-04-02", "2018-04-03", "2018-04-04", "2018-04-05",
			"2018-04-06", "2018-04-07", "2018-04-08", "2018-04-09", "2018-04-10",
		}
		got := buildWhoop(nil, testRecoveryEngine(), now, havana)
		if got == nil {
			t.Fatal("connected user should always get a section")
			return
		}
		if !reflect.DeepEqual(chartedDates(got.Days), want) {
			t.Fatalf("Days dates = %v, want %v", chartedDates(got.Days), want)
		}
	})

	t.Run("explicit window across the transition", func(t *testing.T) {
		now := time.Date(2018, 3, 14, 9, 0, 0, 0, havana)
		want := []string{
			"2018-03-08", "2018-03-09", "2018-03-10", "2018-03-11",
			"2018-03-12", "2018-03-13", "2018-03-14",
		}
		got := buildRecoveryView(nil, testRecoveryEngine(), "2018-03-08", "2018-03-14", now, havana)
		if got == nil {
			t.Fatal("connected user should always get a section")
			return
		}
		if !reflect.DeepEqual(chartedDates(got.Days), want) {
			t.Fatalf("Days dates = %v, want %v", chartedDates(got.Days), want)
		}
	})
}

// TestBuildRecoveryView_BaselineSampleIsOneWindowWide: the scalar baseline/HRV
// blocks answer "how does the most recent morning stand against its own
// trailing baseline", so their sample is one BaselineWindowDays regardless of
// how wide the caller charted. Fed the whole charted window instead, a wide
// window averages its baseline over everything and a narrow one starves it —
// either way the blocks stop agreeing with the final charted day's own band.
func TestBuildRecoveryView_BaselineSampleIsOneWindowWide(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	// 91 stored mornings (offsets 90..0) so even a 61-day chart keeps a full
	// lead-in behind its oldest day. Ramped HRV, so every window has a
	// distinguishable mean.
	entries := entriesBetween(now, denver, 90, 0, rampHRV)

	tests := []struct {
		name        string
		from        string
		wantCharted int
	}{
		{"wide window", "2026-06-02", 61},  // offset 60 → today
		{"narrow window", "2026-07-27", 6}, // offset 5 → today
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRecoveryView(entries, testRecoveryEngine(), tt.from, "2026-08-01", now, denver)
			if got == nil {
				t.Fatal("connected user should always get a section")
				return
			}
			if len(got.Days) != tt.wantCharted {
				t.Fatalf("len(Days) = %d, want %d", len(got.Days), tt.wantCharted)
			}

			// The sample is the 30 dates BEFORE today: offsets 30 (oldest) down
			// to 1, i.e. HRV 70..99.
			const win = 30
			if got.Baseline.HRVDays != win {
				t.Errorf("baseline.hrv_days = %d, want %d — the sample followed the charted width, not the baseline window",
					got.Baseline.HRVDays, win)
			}
			if got.Baseline.WindowDays != win {
				t.Errorf("baseline.window_days = %d, want %d", got.Baseline.WindowDays, win)
			}
			var sum float64
			for o := win; o >= 1; o-- {
				sum += rampHRV(o)
			}
			wantAvg := sum / win
			if got.Baseline.HRVAvg == nil || math.Abs(*got.Baseline.HRVAvg-wantAvg) > 1e-9 {
				t.Errorf("baseline.hrv_avg = %v, want %v", got.Baseline.HRVAvg, wantAvg)
			}

			// The scalar blocks describe the final charted day, so they must
			// agree with that day's own band rather than contradict it.
			last := got.Days[len(got.Days)-1]
			if last.BaselineAvg == nil || got.Baseline.HRVAvg == nil || *last.BaselineAvg != *got.Baseline.HRVAvg {
				t.Errorf("Days[last].baseline_avg = %v, want %v (= baseline.hrv_avg)", last.BaselineAvg, got.Baseline.HRVAvg)
			}
			if got.HRV.Status != last.Status {
				t.Errorf("hrv.status = %q, want %q (= Days[last].status)", got.HRV.Status, last.Status)
			}
			if got.HRV.Status == recoverytrend.StatusUnknown {
				t.Error("hrv.status = unknown while the charted days are classified")
			}
		})
	}
}

// TestBuildRecoveryView_MissingMorningsInWideWindow: gaps are slots, not
// omissions, and the order never breaks.
func TestBuildRecoveryView_MissingMorningsInWideWindow(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	present := map[string]float64{"2026-07-01": 70, "2026-07-05": 75, "2026-07-10": 80}
	var entries []whooprecovery.Entry
	for ds, hrv := range present {
		entries = append(entries, whooprecovery.Entry{
			Date:             ds,
			RestingHeartRate: rhrPtr(52),
			RecoveryScore:    rhrPtr(60),
			HRVRmssdMilli:    rhrPtr(hrv),
		})
	}

	got := buildRecoveryView(entries, testRecoveryEngine(), "2026-07-01", "2026-07-10", now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}

	want := []string{
		"2026-07-01", "2026-07-02", "2026-07-03", "2026-07-04", "2026-07-05",
		"2026-07-06", "2026-07-07", "2026-07-08", "2026-07-09", "2026-07-10",
	}
	if !reflect.DeepEqual(chartedDates(got.Days), want) {
		t.Fatalf("Days dates = %v, want %v", chartedDates(got.Days), want)
	}
	for i, day := range got.Days {
		hrv, stored := present[day.Date]
		switch {
		case stored && (day.HRVRmssdMilli == nil || *day.HRVRmssdMilli != hrv):
			t.Errorf("Days[%d] (%s) hrv = %v, want %v", i, day.Date, day.HRVRmssdMilli, hrv)
		case !stored && (day.HRVRmssdMilli != nil || day.RestingHeartRate != nil || day.RecoveryScore != nil):
			t.Errorf("Days[%d] (%s) should carry null metrics, got %+v", i, day.Date, day)
		}
	}
}

// TestBuildRecoveryView_DegenerateWindow: a window with nothing in it charts
// nothing, serializes days as [] rather than null, and does not panic. The
// derived blocks come back from the engine in their unknown state.
func TestBuildRecoveryView_DegenerateWindow(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)
	entries := entriesEndingAt(now, denver, 61, 80)

	tests := []struct {
		name     string
		from, to string
	}{
		{"to before from", "2026-07-10", "2026-07-01"},
		{"to one day before from", "2026-07-10", "2026-07-09"},
		{"unparseable from", "not-a-date", "2026-07-10"},
		{"unparseable to", "2026-07-01", "2026-13-45"},
		{"both empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRecoveryView(entries, testRecoveryEngine(), tt.from, tt.to, now, denver)
			if got == nil {
				t.Fatal("degenerate window still gets a section")
				return
			}
			if len(got.Days) != 0 {
				t.Fatalf("len(Days) = %d, want 0", len(got.Days))
			}
			if got.Days == nil {
				t.Error("Days must be a non-nil empty slice so it marshals as []")
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(m["days"]) != "[]" {
				t.Errorf("days = %s, want []", m["days"])
			}
			if got.HRV.Status != recoverytrend.StatusUnknown || got.HRV.Trend != recoverytrend.TrendUnknown {
				t.Errorf("hrv status/trend = %q/%q, want unknown/unknown", got.HRV.Status, got.HRV.Trend)
			}
			if got.Baseline.HRVAvg != nil || got.Baseline.HRVDays != 0 {
				t.Errorf("baseline avg/days = %v/%d, want nil/0", got.Baseline.HRVAvg, got.Baseline.HRVDays)
			}
			if got.BaselineTrend.Direction != recoverytrend.TrendUnknown {
				t.Errorf("baseline_trend.direction = %q, want unknown", got.BaselineTrend.Direction)
			}
			// Today and the spark are built from now, so they survive.
			if got.Today == nil || got.Today.Date != "2026-08-01" {
				t.Errorf("today = %+v, want the 2026-08-01 row", got.Today)
			}
		})
	}
}

// TestBuildWhoop_DelegatesToDashboardWindow pins the wrapper's window: the
// dashboard's is the last baseline_window_days + 1 dates ending today, and
// stating it explicitly must produce the same section.
func TestBuildWhoop_DelegatesToDashboardWindow(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)
	entries := entriesBetween(now, denver, 60, 0, rampHRV)

	want := buildWhoop(entries, testRecoveryEngine(), now, denver)
	got := buildRecoveryView(entries, testRecoveryEngine(), "2026-07-02", "2026-08-01", now, denver)
	if !reflect.DeepEqual(got, want) {
		t.Error("buildWhoop must be buildRecoveryView over [today-30, today]")
	}
}
