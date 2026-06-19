package dashboard

import (
	"reflect"
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestLocalWeekStart(t *testing.T) {
	denver := mustLoad(t, "America/Denver")

	tests := []struct {
		name string
		in   time.Time
		loc  *time.Location
		want time.Time
	}{
		{
			// Wednesday 2026-06-17 -> Monday 2026-06-15 local.
			name: "midweek denver",
			in:   time.Date(2026, 6, 17, 13, 0, 0, 0, denver),
			loc:  denver,
			want: time.Date(2026, 6, 15, 0, 0, 0, 0, denver),
		},
		{
			// Monday itself stays on its own day at 00:00.
			name: "monday is its own start",
			in:   time.Date(2026, 6, 15, 9, 30, 0, 0, denver),
			loc:  denver,
			want: time.Date(2026, 6, 15, 0, 0, 0, 0, denver),
		},
		{
			// Sunday 23:00 local belongs to the week starting the prior Monday.
			name: "sunday late belongs to same week",
			in:   time.Date(2026, 6, 21, 23, 0, 0, 0, denver),
			loc:  denver,
			want: time.Date(2026, 6, 15, 0, 0, 0, 0, denver),
		},
		{
			// Same instant differs UTC vs Denver across the boundary: a run at
			// 2026-06-15 05:00 UTC is still Sunday 23:00 in Denver (UTC-6), so
			// its Denver week starts the prior Monday (06-08) while its UTC
			// week starts 06-15.
			name: "denver vs utc boundary - denver",
			in:   time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC),
			loc:  denver,
			want: time.Date(2026, 6, 8, 0, 0, 0, 0, denver),
		},
		{
			name: "denver vs utc boundary - utc",
			in:   time.Date(2026, 6, 15, 5, 0, 0, 0, time.UTC),
			loc:  time.UTC,
			want: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "nil loc defaults to utc",
			in:   time.Date(2026, 6, 17, 13, 0, 0, 0, time.UTC),
			loc:  nil,
			want: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := localWeekStart(tc.in, tc.loc)
			if !got.Equal(tc.want) {
				t.Errorf("localWeekStart = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestWeeklyBucketStarts(t *testing.T) {
	denver := mustLoad(t, "America/Denver")
	now := time.Date(2026, 6, 17, 13, 0, 0, 0, denver) // Wednesday

	got := weeklyBucketStarts(now, denver, 8)
	if len(got) != 8 {
		t.Fatalf("len = %d, want 8", len(got))
	}
	// Ascending, ends with current week's Monday, contiguous 7 days apart.
	wantLast := time.Date(2026, 6, 15, 0, 0, 0, 0, denver)
	if !got[7].Equal(wantLast) {
		t.Errorf("last bucket = %s, want %s", got[7], wantLast)
	}
	for i := 1; i < len(got); i++ {
		if !got[i-1].Before(got[i]) {
			t.Errorf("buckets not ascending at %d: %s !< %s", i, got[i-1], got[i])
		}
		if d := got[i].Sub(got[i-1]); d < 6*24*time.Hour || d > 8*24*time.Hour {
			t.Errorf("gap at %d = %s, want ~7 days", i, d)
		}
	}

	if weeklyBucketStarts(now, denver, 0) != nil {
		t.Error("weeks=0 should be nil")
	}
	if weeklyBucketStarts(now, denver, -3) != nil {
		t.Error("negative weeks should be nil")
	}
}

func TestDownsampleFloats(t *testing.T) {
	tests := []struct {
		name string
		xs   []float64
		max  int
		want []float64
	}{
		{"fits unchanged", []float64{1, 2, 3}, 5, []float64{1, 2, 3}},
		{"equal length unchanged", []float64{1, 2, 3}, 3, []float64{1, 2, 3}},
		{"empty", []float64{}, 4, []float64{}},
		{"nil", nil, 4, nil},
		{"single", []float64{9}, 4, []float64{9}},
		{"zero cap", []float64{1, 2, 3}, 0, nil},
		{"negative cap", []float64{1, 2, 3}, -1, nil},
		{"cap one keeps last", []float64{1, 2, 3, 4}, 1, []float64{4}},
		{"downsample keeps endpoints", []float64{0, 1, 2, 3, 4, 5, 6}, 3, []float64{0, 3, 6}},
		{"downsample five from nine", []float64{0, 1, 2, 3, 4, 5, 6, 7, 8}, 5, []float64{0, 2, 4, 6, 8}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := downsampleFloats(tc.xs, tc.max)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("downsampleFloats(%v, %d) = %v, want %v", tc.xs, tc.max, got, tc.want)
			}
			// First/last preserved whenever there's output.
			if len(got) > 0 && len(tc.xs) > 0 && tc.max > 1 {
				if got[0] != tc.xs[0] || got[len(got)-1] != tc.xs[len(tc.xs)-1] {
					t.Errorf("endpoints not preserved: got %v from %v", got, tc.xs)
				}
			}
		})
	}
}
