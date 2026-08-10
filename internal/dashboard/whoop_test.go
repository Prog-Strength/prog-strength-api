package dashboard

import (
	"encoding/json"
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

// entriesEndingAt builds n consecutive daily whoop entries ending on the local
// date of now, oldest first, with a constant HRV.
func entriesEndingAt(now time.Time, loc *time.Location, n int, hrv float64) []whooprecovery.Entry {
	local := now.In(loc)
	y, mo, d := local.Date()
	out := make([]whooprecovery.Entry, 0, n)
	for i := n - 1; i >= 0; i-- {
		day := time.Date(y, mo, d-i, 0, 0, 0, 0, loc)
		out = append(out, whooprecovery.Entry{
			Date:             day.Format("2006-01-02"),
			RestingHeartRate: rhrPtr(52),
			RecoveryScore:    rhrPtr(65),
			HRVRmssdMilli:    rhrPtr(hrv),
		})
	}
	return out
}

// entriesBetween builds daily entries for the local dates from `fromOffset`
// days before now down to `toOffset` days before now (inclusive), oldest first.
func entriesBetween(now time.Time, loc *time.Location, fromOffset, toOffset int, hrv float64) []whooprecovery.Entry {
	local := now.In(loc)
	y, mo, d := local.Date()
	out := make([]whooprecovery.Entry, 0, fromOffset-toOffset+1)
	for i := fromOffset; i >= toOffset; i-- {
		day := time.Date(y, mo, d-i, 0, 0, 0, 0, loc)
		out = append(out, whooprecovery.Entry{
			Date:             day.Format("2006-01-02"),
			RestingHeartRate: rhrPtr(52),
			RecoveryScore:    rhrPtr(65),
			HRVRmssdMilli:    rhrPtr(hrv),
		})
	}
	return out
}

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
	wide := append(entriesBetween(now, denver, 60, 31, 200), narrow...)
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

// TestBuildWhoop_DaysCarryPerDayBands pins the days[last]-agrees-with-the-
// scalar-block invariant: the final charted day's band is computed from exactly
// the window Compute uses for today.
func TestBuildWhoop_DaysCarryPerDayBands(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, denver)

	got := buildWhoop(entriesEndingAt(now, denver, 61, 80), testRecoveryEngine(), now, denver)
	if got == nil {
		t.Fatal("connected user should always get a section")
		return
	}
	if len(got.Days) != 31 {
		t.Fatalf("len(Days) = %d, want 31 charted days", len(got.Days))
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
	if len(got.Days) == 0 || got.Days[0].Date != oldest {
		t.Fatalf("Days[0].date = %v, want %s", got.Days, oldest)
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
