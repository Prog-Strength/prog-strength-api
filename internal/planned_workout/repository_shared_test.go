package plannedworkout

import (
	"context"
	"errors"
	"testing"
	"time"
)

func ptrStr(s string) *string                    { return &s }
func ptrInt(i int) *int                          { return &i }
func ptrF(f float64) *float64                    { return &f }
func ptrKind(k SessionKind) *SessionKind         { return &k }
func ptrDetail(d CalendarDetail) *CalendarDetail { return &d }

// newPlan builds a minimal valid bare time-block plan (no agenda) for the
// given owner and window start. Callers tweak fields as needed.
func newPlan(userID string, start time.Time) *PlannedWorkout {
	return &PlannedWorkout{
		UserID:            userID,
		ActivityKind:      ActivityKindLift,
		ScheduledStartUTC: start,
		ScheduledEndUTC:   start.Add(time.Hour),
		Timezone:          "America/New_York",
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tt.UTC()
}

// runRepositoryContract exercises a Repository implementation against the
// full behavioral contract. Both the memory and sqlite repos run it via a
// factory so the two implementations can't drift.
func runRepositoryContract(t *testing.T, newRepo func(t *testing.T) Repository) {
	t.Helper()

	t.Run("CreateBareTimeBlock_DefaultsPlannedNoAgenda", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-20T17:00:00Z"))
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if pw.ID == "" {
			t.Fatal("Create did not populate ID")
		}
		if pw.CreatedAt.IsZero() || pw.UpdatedAt.IsZero() {
			t.Fatal("Create did not stamp timestamps")
		}

		got, err := repo.Get(ctx, "u1", pw.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != StatusPlanned {
			t.Errorf("status = %q, want planned (default)", got.Status)
		}
		if len(got.Exercises) != 0 {
			t.Errorf("expected empty agenda, got %+v", got.Exercises)
		}
	})

	t.Run("CreateWithAgenda_HydratesInOrder", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-21T17:00:00Z"))
		pw.Name = ptrStr("Push Day")
		pw.CalendarDetail = ptrDetail(DetailFullAgenda)
		pw.Exercises = []PlannedExercise{
			{
				ExerciseID: "bench",
				Notes:      ptrStr("paused"),
				Sets: []PlannedSet{
					{TargetReps: ptrInt(5), TargetWeight: ptrF(185), Unit: ptrStr("lb")},
					{TargetReps: ptrInt(5), TargetWeight: nil, Unit: ptrStr("lb"), TargetRPE: ptrF(8)},
				},
			},
			{
				ExerciseID: "ohp",
				Sets: []PlannedSet{
					{TargetReps: ptrInt(8), TargetWeight: ptrF(95), Unit: ptrStr("lb")},
				},
			},
		}
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := repo.Get(ctx, "u1", pw.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Exercises) != 2 {
			t.Fatalf("want 2 exercises, got %d", len(got.Exercises))
		}
		if got.Exercises[0].ExerciseID != "bench" || got.Exercises[1].ExerciseID != "ohp" {
			t.Fatalf("exercise order wrong: %+v", got.Exercises)
		}
		if got.Exercises[0].OrderIndex != 0 || got.Exercises[1].OrderIndex != 1 {
			t.Fatalf("order_index not set by position: %+v", got.Exercises)
		}
		if got.Exercises[0].Notes == nil || *got.Exercises[0].Notes != "paused" {
			t.Fatalf("exercise notes not persisted: %+v", got.Exercises[0].Notes)
		}
		bench := got.Exercises[0]
		if len(bench.Sets) != 2 {
			t.Fatalf("want 2 sets on bench, got %d", len(bench.Sets))
		}
		if bench.Sets[0].OrderIndex != 0 || bench.Sets[1].OrderIndex != 1 {
			t.Fatalf("set order_index wrong: %+v", bench.Sets)
		}
		if bench.Sets[1].TargetWeight != nil {
			t.Errorf("expected nil TargetWeight preserved, got %v", *bench.Sets[1].TargetWeight)
		}
		if bench.Sets[1].TargetRPE == nil || *bench.Sets[1].TargetRPE != 8 {
			t.Errorf("TargetRPE not round-tripped: %+v", bench.Sets[1].TargetRPE)
		}
		if bench.Sets[0].TargetReps == nil || *bench.Sets[0].TargetReps != 5 {
			t.Errorf("TargetReps not round-tripped: %+v", bench.Sets[0].TargetReps)
		}
		if got.Name == nil || *got.Name != "Push Day" {
			t.Errorf("name not persisted: %+v", got.Name)
		}
		if got.CalendarDetail == nil || *got.CalendarDetail != DetailFullAgenda {
			t.Errorf("calendar detail not persisted: %+v", got.CalendarDetail)
		}
	})

	t.Run("List_RangeOrderingOwnershipAndSoftDelete", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		mk := func(user, start string) *PlannedWorkout {
			pw := newPlan(user, mustTime(t, start))
			if err := repo.Create(ctx, pw); err != nil {
				t.Fatalf("seed Create: %v", err)
			}
			return pw
		}
		// Out-of-order insertion to prove the query sorts.
		mid := mk("u1", "2026-06-11T17:00:00Z")
		early := mk("u1", "2026-06-10T17:00:00Z")
		late := mk("u1", "2026-06-12T17:00:00Z")
		// In-range but soft-deleted → excluded.
		del := mk("u1", "2026-06-11T18:00:00Z")
		if err := repo.Delete(ctx, "u1", del.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// Other user's in-range plan → excluded.
		mk("u2", "2026-06-11T17:00:00Z")

		since := mustTime(t, "2026-06-10T00:00:00Z")
		until := mustTime(t, "2026-06-12T00:00:00Z") // exclusive: late at 6-12 excluded
		got, err := repo.List(ctx, "u1", &since, &until)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 in-range live plans, got %d: %+v", len(got), got)
		}
		if got[0].ID != early.ID || got[1].ID != mid.ID {
			t.Fatalf("want ASC by start [early, mid], got [%s, %s]", got[0].ID, got[1].ID)
		}

		// nil bounds → all of u1's live plans (early, mid, late = 3).
		all, err := repo.List(ctx, "u1", nil, nil)
		if err != nil {
			t.Fatalf("List unbounded: %v", err)
		}
		if len(all) != 3 {
			t.Fatalf("want 3 unbounded live plans, got %d", len(all))
		}
		if all[0].ID != early.ID || all[2].ID != late.ID {
			t.Fatalf("unbounded order wrong: %+v", all)
		}
	})

	t.Run("Update_RescheduleReplacesAgendaPreservesCreatedAt", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-20T17:00:00Z"))
		pw.Exercises = []PlannedExercise{
			{ExerciseID: "squat", Sets: []PlannedSet{{TargetReps: ptrInt(5)}}},
		}
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}
		origCreated := pw.CreatedAt

		// Reschedule + swap the agenda entirely.
		pw.ScheduledStartUTC = mustTime(t, "2026-06-22T18:00:00Z")
		pw.ScheduledEndUTC = pw.ScheduledStartUTC.Add(90 * time.Minute)
		pw.Name = ptrStr("Renamed")
		pw.Exercises = []PlannedExercise{
			{ExerciseID: "deadlift", Sets: []PlannedSet{{TargetReps: ptrInt(3), TargetWeight: ptrF(315), Unit: ptrStr("lb")}}},
			{ExerciseID: "row", Sets: nil},
		}
		if err := repo.Update(ctx, pw); err != nil {
			t.Fatalf("Update: %v", err)
		}

		got, err := repo.Get(ctx, "u1", pw.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.ScheduledStartUTC.Equal(mustTime(t, "2026-06-22T18:00:00Z")) {
			t.Errorf("reschedule not applied: %v", got.ScheduledStartUTC)
		}
		if len(got.Exercises) != 2 || got.Exercises[0].ExerciseID != "deadlift" || got.Exercises[1].ExerciseID != "row" {
			t.Fatalf("agenda not replaced: %+v", got.Exercises)
		}
		if got.Name == nil || *got.Name != "Renamed" {
			t.Errorf("name not updated: %+v", got.Name)
		}
		if !got.CreatedAt.Equal(origCreated) {
			t.Errorf("created_at not preserved: got %v want %v", got.CreatedAt, origCreated)
		}
	})

	t.Run("Delete_SoftDeletesAndDisappears", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-20T17:00:00Z"))
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := repo.Delete(ctx, "u1", pw.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := repo.Get(ctx, "u1", pw.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after delete: want ErrNotFound, got %v", err)
		}
		got, err := repo.List(ctx, "u1", nil, nil)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("soft-deleted plan should not list, got %+v", got)
		}
		if err := repo.Delete(ctx, "u1", pw.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("double delete: want ErrNotFound, got %v", err)
		}
	})

	t.Run("SetStatus_Skipped_AndCrossUser", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-20T17:00:00Z"))
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := repo.SetStatus(ctx, "u1", pw.ID, StatusSkipped); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		got, err := repo.Get(ctx, "u1", pw.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != StatusSkipped {
			t.Errorf("status = %q, want skipped", got.Status)
		}
		if err := repo.SetStatus(ctx, "u2", pw.ID, StatusPlanned); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-user SetStatus: want ErrNotFound, got %v", err)
		}
	})

	t.Run("SetCompletion_SetsStatusAndLink", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-20T17:00:00Z"))
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := repo.SetCompletion(ctx, "u1", pw.ID, "sess-123", SessionKindWorkout); err != nil {
			t.Fatalf("SetCompletion: %v", err)
		}
		got, err := repo.Get(ctx, "u1", pw.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != StatusCompleted {
			t.Errorf("status = %q, want completed", got.Status)
		}
		if got.CompletedSessionID == nil || *got.CompletedSessionID != "sess-123" {
			t.Errorf("session id not linked: %+v", got.CompletedSessionID)
		}
		if got.CompletedSessionKind == nil || *got.CompletedSessionKind != SessionKindWorkout {
			t.Errorf("session kind not linked: %+v", got.CompletedSessionKind)
		}
		if err := repo.SetCompletion(ctx, "u2", pw.ID, "x", SessionKindWorkout); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-user SetCompletion: want ErrNotFound, got %v", err)
		}
	})

	t.Run("SetGoogleSync_RoundTripsAndNilClears", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-20T17:00:00Z"))
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}

		// Failure path: event id set, status failed, error recorded.
		if err := repo.SetGoogleSync(ctx, "u1", pw.ID, ptrStr("evt-1"), SyncFailed, ptrStr("boom")); err != nil {
			t.Fatalf("SetGoogleSync failed-path: %v", err)
		}
		got, err := repo.Get(ctx, "u1", pw.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.GoogleEventID == nil || *got.GoogleEventID != "evt-1" {
			t.Errorf("event id not stored: %+v", got.GoogleEventID)
		}
		if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != SyncFailed {
			t.Errorf("sync status not stored: %+v", got.GoogleSyncStatus)
		}
		if got.LastSyncError == nil || *got.LastSyncError != "boom" {
			t.Errorf("last sync error not stored: %+v", got.LastSyncError)
		}

		// Success path: nil event id clears it, error cleared.
		if err := repo.SetGoogleSync(ctx, "u1", pw.ID, nil, SyncSynced, nil); err != nil {
			t.Fatalf("SetGoogleSync synced-path: %v", err)
		}
		got, err = repo.Get(ctx, "u1", pw.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.GoogleEventID != nil {
			t.Errorf("event id should be cleared, got %v", *got.GoogleEventID)
		}
		if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != SyncSynced {
			t.Errorf("sync status not updated: %+v", got.GoogleSyncStatus)
		}
		if got.LastSyncError != nil {
			t.Errorf("last sync error should be cleared, got %v", *got.LastSyncError)
		}

		if err := repo.SetGoogleSync(ctx, "u2", pw.ID, nil, SyncSynced, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-user SetGoogleSync: want ErrNotFound, got %v", err)
		}
	})

	t.Run("CrossUser_GetUpdateDelete", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		pw := newPlan("u1", mustTime(t, "2026-06-20T17:00:00Z"))
		if err := repo.Create(ctx, pw); err != nil {
			t.Fatalf("Create: %v", err)
		}

		if _, err := repo.Get(ctx, "u2", pw.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-user Get: want ErrNotFound, got %v", err)
		}
		other := *pw
		other.UserID = "u2"
		if err := repo.Update(ctx, &other); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-user Update: want ErrNotFound, got %v", err)
		}
		if err := repo.Delete(ctx, "u2", pw.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-user Delete: want ErrNotFound, got %v", err)
		}
	})
}
