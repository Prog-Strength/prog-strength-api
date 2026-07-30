package activity

import (
	"context"
	"errors"
	"testing"
)

// TestHiking_RoundTripsElevationTriple creates a hiking activity through the
// SQLite repository, confirms the elevation triple round-trips on List/Get,
// asserts the detail row landed in activity_hike_details (the fifth endurance
// detail table wired by migration 044), then soft-deletes and confirms it's
// gone. Exercises the whole endurance seam for the new type end to end.
func TestHiking_RoundTripsElevationTriple(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	a := &Activity{
		UserID:              "u1",
		ActivityType:        ActivityHiking,
		IngestSource:        IngestManualTCX,
		SourceActivityID:    "hike-1",
		StartTime:           mustTime(t, "2026-07-15T06:30:00Z"),
		Name:                ptrStr("Bear Peak"),
		DistanceMeters:      10783, // ~6.7 mi
		RawDistanceMeters:   10783,
		Environment:         EnvironmentOutdoor,
		DurationSeconds:     5*3600 + 12*60 + 40,
		ElevationGainMeters: ptrF(1051.56),
		ElevationLossMeters: ptrF(1051.56),
		ElevationHighMeters: ptrF(2565),
		ElevationLowMeters:  ptrF(1740),
	}
	if err := repo.Create(ctx, a, []byte("<TrainingCenterDatabase/>")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	assertTriple := func(label string, got *Activity) {
		t.Helper()
		if got.ActivityType != ActivityHiking {
			t.Errorf("%s: ActivityType = %q, want hiking", label, got.ActivityType)
		}
		if deref(got.ElevationGainMeters) != 1051.56 {
			t.Errorf("%s: gain = %v, want 1051.56", label, got.ElevationGainMeters)
		}
		if deref(got.ElevationLossMeters) != 1051.56 {
			t.Errorf("%s: loss = %v, want 1051.56", label, got.ElevationLossMeters)
		}
		if deref(got.ElevationHighMeters) != 2565 {
			t.Errorf("%s: high = %v, want 2565", label, got.ElevationHighMeters)
		}
		if deref(got.ElevationLowMeters) != 1740 {
			t.Errorf("%s: low = %v, want 1740", label, got.ElevationLowMeters)
		}
	}

	got, err := repo.Get(ctx, "u1", a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertTriple("Get", got)

	list, err := repo.List(ctx, "u1", 10, nil, TypeFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List = %d items, want 1", len(list))
	}
	assertTriple("List", &list[0])

	// The row must live in activity_hike_details, not any other detail table.
	var count int
	if err := repo.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM activity_hike_details WHERE activity_id = ?`, a.ID).Scan(&count); err != nil {
		t.Fatalf("count hike details: %v", err)
	}
	if count != 1 {
		t.Fatalf("activity_hike_details rows = %d, want 1", count)
	}

	if err := repo.SoftDelete(ctx, "u1", a.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := repo.Get(ctx, "u1", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrNotFound", err)
	}
}
