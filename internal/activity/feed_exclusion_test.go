package activity

import (
	"context"
	"testing"
	"time"
)

// TestFeed_TypeFilter proves the TypeFilter contract on List/ListInRange:
// the zero value reads every type (the unified /activities list, strength
// included) and Only narrows to one type. SummariesByIDs is filter-free
// (the workout list's enrichment read), and the running-only metrics path
// is unaffected either way.
func TestFeed_TypeFilter(t *testing.T) {
	t.Parallel()
	repo, _ := newRepo(t)
	ctx := context.Background()

	run := newActivity("u1", IngestManualTCX, "run1", mustTime(t, "2026-06-19T07:00:00Z"), 5000, 1500)
	if err := repo.Create(ctx, run, []byte("run")); err != nil {
		t.Fatalf("create run: %v", err)
	}

	strength := &Activity{
		UserID:           "u1",
		ActivityType:     ActivityStrengthTraining,
		IngestSource:     IngestManualTCX,
		SourceActivityID: "str1",
		StartTime:        mustTime(t, "2026-06-19T13:00:00Z"),
		DistanceMeters:   0,
		DurationSeconds:  1800,
		AvgHeartRateBpm:  ptrInt(138),
		MaxHeartRateBpm:  ptrInt(172),
		TotalCalories:    ptrInt(240),
		Trackpoints: []Trackpoint{
			{Sequence: 0, ElapsedSeconds: 0, HeartRateBpm: ptrInt(120)},
			{Sequence: 1, ElapsedSeconds: 30, HeartRateBpm: ptrInt(150)},
		},
	}
	if err := repo.Create(ctx, strength, []byte("strength")); err != nil {
		t.Fatalf("create strength: %v", err)
	}

	// The zero-value filter is the unified list: every type, lift included.
	listed, err := repo.List(ctx, "u1", 50, nil, TypeFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("List (all types) = %d rows, want 2 incl. strength", len(listed))
	}

	// Only narrows to one type (the ?type= filter).
	listed, err = repo.List(ctx, "u1", 50, nil, TypeFilter{Only: ActivityStrengthTraining})
	if err != nil {
		t.Fatalf("list only strength: %v", err)
	}
	if len(listed) != 1 || listed[0].ActivityType != ActivityStrengthTraining {
		t.Fatalf("List (only strength) = %d rows, want the strength row", len(listed))
	}

	// ListInRange honors the same filter.
	since := mustTime(t, "2026-06-19T00:00:00Z")
	until := mustTime(t, "2026-06-20T00:00:00Z")
	ranged, err := repo.ListInRange(ctx, "u1", &since, &until, TypeFilter{})
	if err != nil {
		t.Fatalf("list in range all: %v", err)
	}
	if len(ranged) != 2 {
		t.Fatalf("ListInRange (all types) = %d rows, want 2", len(ranged))
	}
	ranged, err = repo.ListInRange(ctx, "u1", &since, &until, TypeFilter{Only: ActivityStrengthTraining})
	if err != nil {
		t.Fatalf("list in range only strength: %v", err)
	}
	if len(ranged) != 1 || ranged[0].ActivityType != ActivityStrengthTraining {
		t.Fatalf("ListInRange (only strength) = %d rows, want 1 strength row", len(ranged))
	}

	// SummariesByIDs DOES return the strength row (workout-enrichment read).
	summaries, err := repo.SummariesByIDs(ctx, "u1", []string{run.ID, strength.ID})
	if err != nil {
		t.Fatalf("summaries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("SummariesByIDs = %d, want 2 (incl. strength)", len(summaries))
	}
	s, ok := summaries[strength.ID]
	if !ok {
		t.Fatal("SummariesByIDs missing strength activity")
	}
	if s.TotalCalories == nil || *s.TotalCalories != 240 {
		t.Errorf("strength calories = %v, want 240", s.TotalCalories)
	}
	if len(s.Trackpoints) != 0 {
		t.Errorf("SummariesByIDs trackpoints = %d, want 0 (summary only)", len(s.Trackpoints))
	}

	// Running metrics are over running rows only — strength can't contribute.
	m, err := repo.RunningMetrics(ctx, "u1", time.Now().UTC(), time.UTC)
	if err != nil {
		t.Fatalf("running metrics: %v", err)
	}
	if m.AllTime.RunCount != 1 {
		t.Errorf("all-time run_count = %d, want 1 (strength excluded)", m.AllTime.RunCount)
	}
}
