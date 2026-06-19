package dashboard

import (
	"testing"
	"time"
)

// activeSet builds an activeDates set from date strings.
func activeSet(dates ...string) map[string]bool {
	m := make(map[string]bool, len(dates))
	for _, d := range dates {
		m[d] = true
	}
	return m
}

func TestBuildStreak_EmptyIsZero(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	got := buildStreak(nil, now, denver)
	if got.Weeks != 0 || got.ActiveDaysThisWeek != 0 {
		t.Errorf("empty: %+v, want zero", got)
	}
	for i, v := range got.Week {
		if v {
			t.Errorf("week[%d] should be false", i)
		}
	}
}

func TestBuildStreak_CurrentWeekDaysAndBoundary(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	// Wednesday 2026-06-17. Week is Mon 06-15 .. Sun 06-21.
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// 06-14 (Sunday) lands in the PRIOR local week (Mon 06-08..Sun 06-14),
	// not this one — proving the Mon-anchored week boundary. In-week active
	// days: Mon 06-15, Wed 06-17.
	got := buildStreak(activeSet("2026-06-15", "2026-06-17", "2026-06-14"), now, denver)

	want := [7]bool{true, false, true, false, false, false, false}
	if got.Week != want {
		t.Errorf("week = %v, want %v", got.Week, want)
	}
	if got.ActiveDaysThisWeek != 2 {
		t.Errorf("active days = %d, want 2", got.ActiveDaysThisWeek)
	}
	// 06-14 (local Sunday) belongs to the prior week, so that week is also
	// active → the run is current + prior = 2 weeks.
	if got.Weeks != 2 {
		t.Errorf("weeks = %d, want 2", got.Weeks)
	}
}

func TestBuildStreak_ConsecutiveWeeks(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// Active in current week (06-15), prior week (06-10), and the week before
	// (06-03). Three consecutive active weeks.
	got := buildStreak(activeSet("2026-06-15", "2026-06-10", "2026-06-03"), now, denver)
	if got.Weeks != 3 {
		t.Errorf("weeks = %d, want 3", got.Weeks)
	}
}

func TestBuildStreak_CurrentInactivePriorCounts(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// Nothing this week (06-15..06-21), but trained the two prior weeks.
	got := buildStreak(activeSet("2026-06-10", "2026-06-03"), now, denver)
	if got.ActiveDaysThisWeek != 0 {
		t.Errorf("active days = %d, want 0", got.ActiveDaysThisWeek)
	}
	if got.Weeks != 2 {
		t.Errorf("weeks = %d, want 2 (prior run when current paused)", got.Weeks)
	}
}

func TestBuildStreak_GapBreaksRun(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver)
	// Current week active, prior week empty, week before active. Gap breaks
	// the run at the prior week → streak is just the current week.
	got := buildStreak(activeSet("2026-06-15", "2026-06-03"), now, denver)
	if got.Weeks != 1 {
		t.Errorf("weeks = %d, want 1 (gap breaks run)", got.Weeks)
	}
}
