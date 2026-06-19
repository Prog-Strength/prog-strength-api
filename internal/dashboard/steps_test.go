package dashboard

import (
	"reflect"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
)

func TestBuildSteps_EmptyReturnsNil(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	if got := buildSteps(nil, steps.Goal{}, now, denver); got != nil {
		t.Errorf("no entries should be nil, got %+v", got)
	}
}

func TestBuildSteps_WindowAvgAndToday(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	// Wednesday midweek, late local evening to exercise the local-day boundary:
	// 2026-06-17 23:30 Denver is 2026-06-18 05:30 UTC, so a naive UTC "today"
	// would be wrong.
	now := time.Date(2026, 6, 17, 23, 30, 0, 0, denver)

	// Window is the 7 days 06-11 .. 06-17 (oldest→newest).
	entries := []steps.Entry{
		{Date: "2026-06-11", Steps: 1000},
		{Date: "2026-06-14", Steps: 2000},
		{Date: "2026-06-17", Steps: 4000}, // today (local)
		{Date: "2026-06-18", Steps: 9999}, // future/out-of-window, ignored
		{Date: "2026-06-04", Steps: 5555}, // older than window, ignored
	}

	got := buildSteps(entries, steps.Goal{}, now, denver)
	if got == nil {
		t.Fatal("expected section")
	}
	wantSpark := []int{1000, 0, 0, 2000, 0, 0, 4000}
	if !reflect.DeepEqual(got.DailySpark, wantSpark) {
		t.Errorf("spark = %v, want %v", got.DailySpark, wantSpark)
	}
	if got.Today != 4000 {
		t.Errorf("today = %d, want 4000", got.Today)
	}
	// mean of 7000 over 7 = 1000.
	if got.Avg != 1000 {
		t.Errorf("avg = %d, want 1000", got.Avg)
	}
	if got.Goal != nil {
		t.Errorf("goal should be nil when unset, got %v", *got.Goal)
	}
}

func TestBuildSteps_NoTodayEntry(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	entries := []steps.Entry{{Date: "2026-06-12", Steps: 700}}
	got := buildSteps(entries, steps.Goal{}, now, denver)
	if got == nil {
		t.Fatal("expected section")
	}
	if got.Today != 0 {
		t.Errorf("today = %d, want 0", got.Today)
	}
}

func TestBuildSteps_GoalSetAndZero(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	entries := []steps.Entry{{Date: "2026-06-17", Steps: 100}}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Set goal.
	got := buildSteps(entries, steps.Goal{Goal: 10000, CreatedAt: &ts}, now, denver)
	if got.Goal == nil || *got.Goal != 10000 {
		t.Errorf("goal = %v, want 10000", got.Goal)
	}

	// Present timestamp but zero count → treated as unset.
	got = buildSteps(entries, steps.Goal{Goal: 0, CreatedAt: &ts}, now, denver)
	if got.Goal != nil {
		t.Errorf("zero goal should be nil, got %v", *got.Goal)
	}
}
