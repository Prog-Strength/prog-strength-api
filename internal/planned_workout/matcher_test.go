package plannedworkout

import (
	"testing"
	"time"
)

func mkPlan(id string, kind ActivityKind, status Status, startUTC time.Time, tz string) PlannedWorkout {
	return PlannedWorkout{
		ID:                id,
		UserID:            "u1",
		ActivityKind:      kind,
		Status:            status,
		ScheduledStartUTC: startUTC,
		ScheduledEndUTC:   startUTC.Add(time.Hour),
		Timezone:          tz,
	}
}

func TestSelectPlan(t *testing.T) {
	utc := func(y int, mo time.Month, d, h, mi int) time.Time {
		return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	}
	const ny = "America/New_York"

	tests := []struct {
		name   string
		plans  []PlannedWorkout
		start  time.Time
		kind   SessionKind
		wantID string
	}{
		{
			name:   "same-day match",
			plans:  []PlannedWorkout{mkPlan("p1", ActivityKindRun, StatusPlanned, utc(2026, 6, 15, 17, 30), ny)},
			start:  utc(2026, 6, 15, 18, 0),
			kind:   SessionKindActivity,
			wantID: "p1",
		},
		{
			name:   "off-schedule same local day still matches",
			plans:  []PlannedWorkout{mkPlan("p1", ActivityKindRun, StatusPlanned, utc(2026, 6, 15, 13, 0), ny)},
			start:  utc(2026, 6, 15, 23, 0), // 7pm NY, same NY day as 9am NY plan
			kind:   SessionKindActivity,
			wantID: "p1",
		},
		{
			name: "two-a-day nearest scheduled start wins",
			plans: []PlannedWorkout{
				mkPlan("early", ActivityKindRun, StatusPlanned, utc(2026, 6, 15, 11, 0), ny),
				mkPlan("late", ActivityKindRun, StatusPlanned, utc(2026, 6, 15, 22, 0), ny),
			},
			start:  utc(2026, 6, 15, 21, 30),
			kind:   SessionKindActivity,
			wantID: "late",
		},
		{
			name:   "wrong kind lift plan vs run session no match",
			plans:  []PlannedWorkout{mkPlan("p1", ActivityKindLift, StatusPlanned, utc(2026, 6, 15, 17, 30), ny)},
			start:  utc(2026, 6, 15, 18, 0),
			kind:   SessionKindActivity,
			wantID: "",
		},
		{
			name:   "already completed plan not a candidate",
			plans:  []PlannedWorkout{mkPlan("p1", ActivityKindRun, StatusCompleted, utc(2026, 6, 15, 17, 30), ny)},
			start:  utc(2026, 6, 15, 18, 0),
			kind:   SessionKindActivity,
			wantID: "",
		},
		{
			name:   "timezone boundary different local day no match",
			plans:  []PlannedWorkout{mkPlan("p1", ActivityKindRun, StatusPlanned, utc(2026, 6, 15, 2, 0), ny)}, // 10pm Jun 14 NY
			start:  utc(2026, 6, 15, 20, 0),                                                                    // 4pm Jun 15 NY
			kind:   SessionKindActivity,
			wantID: "",
		},
		{
			name:   "no candidates nil plans",
			plans:  nil,
			start:  utc(2026, 6, 15, 18, 0),
			kind:   SessionKindActivity,
			wantID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectPlan(tt.plans, tt.start, tt.kind)
			if tt.wantID == "" {
				if got != nil {
					t.Fatalf("got %s, want nil", got.ID)
				}
				return
			}
			if got == nil || got.ID != tt.wantID {
				t.Fatalf("got %v, want %s", got, tt.wantID)
			}
		})
	}
}
