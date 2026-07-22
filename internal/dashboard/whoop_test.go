package dashboard

import (
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

func rhrPtr(f float64) *float64 { return &f }

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

	got := buildWhoop(entries, now, denver)
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
	got := buildWhoop(entries, now, denver)
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

	got := buildWhoop(nil, now, denver)
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
