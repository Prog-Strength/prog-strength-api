package dashboard

import (
	"encoding/json"
	"reflect"
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
	for _, k := range []string{"today", "resting_hr_spark", "days", "baseline", "hrv"} {
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
}
