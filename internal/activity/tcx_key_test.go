package activity

import (
	"errors"
	"testing"
	"time"
)

func TestBuildTCXKey(t *testing.T) {
	t.Parallel()

	// pacific is the user's local zone for the boundary-test cases. A
	// timestamp that's "yesterday" in Pacific can be "today" in UTC, and
	// we want to confirm the key uses the UTC day.
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load pacific: %v", err)
	}

	cases := []struct {
		name    string
		userID  string
		actType ActivityType
		date    time.Time
		actID   string
		want    string
		wantErr error
	}{
		{
			name:    "normal case",
			userID:  "user_abc123",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 14, 30, 0, 0, time.UTC),
			actID:   "act_xyz789",
			want:    "user_id=user_abc123/activity_type=running/year=2026/month=06/day=08/act_xyz789.tcx",
		},
		{
			name:    "single-digit month and day pad to two",
			userID:  "u1",
			actType: ActivityCycling,
			date:    time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC),
			actID:   "a1",
			want:    "user_id=u1/activity_type=cycling/year=2026/month=01/day=03/a1.tcx",
		},
		{
			name:    "year zero-pads to four digits",
			userID:  "u1",
			actType: ActivityOther,
			date:    time.Date(999, 12, 31, 23, 59, 0, 0, time.UTC),
			actID:   "a1",
			want:    "user_id=u1/activity_type=other/year=0999/month=12/day=31/a1.tcx",
		},
		{
			name:    "UTC midnight: 2026-06-08T00:00Z stays on day 08",
			userID:  "u1",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			want:    "user_id=u1/activity_type=running/year=2026/month=06/day=08/a1.tcx",
		},
		{
			name:    "local late-evening Pacific is next UTC day",
			userID:  "u1",
			actType: ActivityWalking,
			// 2026-06-07T22:00:00-07:00 == 2026-06-08T05:00:00Z
			date:  time.Date(2026, 6, 7, 22, 0, 0, 0, pacific),
			actID: "a1",
			want:  "user_id=u1/activity_type=walking/year=2026/month=06/day=08/a1.tcx",
		},
		{
			name:    "local early-morning Pacific is prior UTC day",
			userID:  "u1",
			actType: ActivityRunning,
			// 2026-06-08T01:00:00-07:00 == 2026-06-08T08:00:00Z (still day 08 in UTC)
			date:  time.Date(2026, 6, 8, 1, 0, 0, 0, pacific),
			actID: "a1",
			want:  "user_id=u1/activity_type=running/year=2026/month=06/day=08/a1.tcx",
		},
		{
			name:    "Pacific midnight crosses into UTC day boundary",
			userID:  "u1",
			actType: ActivityRunning,
			// 2026-06-08T00:00:00-07:00 == 2026-06-08T07:00:00Z (day 08 UTC).
			date:  time.Date(2026, 6, 8, 0, 0, 0, 0, pacific),
			actID: "a1",
			want:  "user_id=u1/activity_type=running/year=2026/month=06/day=08/a1.tcx",
		},
		{
			name:    "userID with slash rejected",
			userID:  "user/abc",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "userID with equals rejected",
			userID:  "user=abc",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "userID with dot rejected",
			userID:  "user.abc",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "userID with whitespace rejected",
			userID:  "user abc",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "empty userID rejected",
			userID:  "",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "activityID with slash rejected",
			userID:  "u1",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a/1",
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "activityID with whitespace rejected",
			userID:  "u1",
			actType: ActivityRunning,
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a 1",
			wantErr: ErrInvalidKeyPart,
		},
		{
			name:    "invalid activityType rejected",
			userID:  "u1",
			actType: ActivityType("swimming"),
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			wantErr: ErrInvalidActivityType,
		},
		{
			name:    "empty activityType rejected",
			userID:  "u1",
			actType: ActivityType(""),
			date:    time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
			actID:   "a1",
			wantErr: ErrInvalidActivityType,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildTCXKey(c.userID, c.actType, c.date, c.actID)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.want {
				t.Errorf("buildTCXKey =\n  %q\nwant\n  %q", got, c.want)
			}
		})
	}
}
