package bloodpressure

import "testing"

func TestClassify(t *testing.T) {
	// Boundary and straddle cases where "higher category wins" and the
	// switch ordering actually matter — plus a clearly-normal and a
	// mid-elevated reading as sanity anchors.
	cases := []struct {
		name                string
		systolic, diastolic int
		want                Category
	}{
		{"clearly normal", 110, 70, CategoryNormal},
		{"normal upper corner", 119, 79, CategoryNormal},
		{"elevated by systolic", 120, 79, CategoryElevated},
		{"mid elevated", 122, 78, CategoryElevated},
		{"stage_1 by systolic", 130, 80, CategoryStage1},
		{"stage_1 straddle low diastolic", 135, 75, CategoryStage1},
		{"stage_1 by diastolic", 125, 85, CategoryStage1},
		{"stage_2 by systolic", 140, 90, CategoryStage2},
		{"stage_2 by diastolic", 115, 95, CategoryStage2},
		{"crisis", 180, 120, CategoryCrisis},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.systolic, tc.diastolic); got != tc.want {
				t.Errorf("Classify(%d, %d) = %q, want %q", tc.systolic, tc.diastolic, got, tc.want)
			}
		})
	}
}
