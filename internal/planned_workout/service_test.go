package plannedworkout

import (
	"context"
	"testing"
	"time"
)

// seedRunPlan seeds a planned-status run plan with the given start (UTC) and tz,
// returning its id.
func seedRunPlan(t *testing.T, repo Repository, userID string, startUTC time.Time, tz string) string {
	t.Helper()
	name := "Easy Run"
	pw := &PlannedWorkout{
		UserID:            userID,
		Name:              &name,
		ActivityKind:      ActivityKindRun,
		ScheduledStartUTC: startUTC,
		ScheduledEndUTC:   startUTC.Add(time.Hour),
		Timezone:          tz,
		Status:            StatusPlanned,
	}
	if err := repo.Create(context.Background(), pw); err != nil {
		t.Fatalf("seed run plan: %v", err)
	}
	return pw.ID
}

// markSynced gives the seeded plan a non-empty Google event id so the
// service's best-effort calendar branch fires.
func markSynced(t *testing.T, repo Repository, userID, planID string) {
	t.Helper()
	eventID := "evt-1"
	if err := repo.SetGoogleSync(context.Background(), userID, planID, &eventID, SyncSynced, nil); err != nil {
		t.Fatalf("set google sync: %v", err)
	}
}

func TestService_LinkCompletion_SyncedRewrites(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	markSynced(t, repo, "u1", id)

	svc := NewService(repo)
	sched := &fakeScheduler{}
	svc.SetCalendar(sched)

	got, err := svc.LinkCompletion(context.Background(), "u1", id, "sess-1", SessionKindWorkout)
	if err != nil {
		t.Fatalf("LinkCompletion: %v", err)
	}
	if got.Status != StatusCompleted {
		t.Errorf("status = %q want completed", got.Status)
	}
	if got.CompletedSessionID == nil || *got.CompletedSessionID != "sess-1" {
		t.Errorf("completed_session_id = %v want sess-1", got.CompletedSessionID)
	}
	if got.CompletedSessionKind == nil || *got.CompletedSessionKind != SessionKindWorkout {
		t.Errorf("completed_session_kind = %v want workout", got.CompletedSessionKind)
	}
	if sched.rewriteCall != 1 {
		t.Fatalf("RewriteCompleted called %d times want 1", sched.rewriteCall)
	}
	if sched.lastRewritePlanID != id {
		t.Errorf("rewrite plan id = %q want %q", sched.lastRewritePlanID, id)
	}
}

func TestService_Unlink_RevertsAndResyncs(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	markSynced(t, repo, "u1", id)

	svc := NewService(repo)
	sched := &fakeScheduler{}
	svc.SetCalendar(sched)

	if _, err := svc.LinkCompletion(context.Background(), "u1", id, "sess-1", SessionKindWorkout); err != nil {
		t.Fatalf("LinkCompletion: %v", err)
	}

	got, err := svc.Unlink(context.Background(), "u1", id)
	if err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if got.Status != StatusPlanned {
		t.Errorf("status = %q want planned", got.Status)
	}
	if got.CompletedSessionID != nil {
		t.Errorf("completed_session_id = %v want nil", got.CompletedSessionID)
	}
	if got.CompletedSessionKind != nil {
		t.Errorf("completed_session_kind = %v want nil", got.CompletedSessionKind)
	}
	if sched.resyncCall != 1 {
		t.Errorf("Resync called %d times want 1", sched.resyncCall)
	}
}

func TestService_OnSessionLogged_MatchesAndLinks(t *testing.T) {
	repo := NewMemoryRepository()
	const ny = "America/New_York"
	planStart := time.Date(2026, 6, 15, 17, 30, 0, 0, time.UTC) // 1:30pm NY
	id := seedRunPlan(t, repo, "u1", planStart, ny)

	svc := NewService(repo)
	svc.SetCalendar(&fakeScheduler{})

	sessionStart := time.Date(2026, 6, 15, 18, 0, 0, 0, time.UTC) // 2pm NY, same NY day
	svc.OnSessionLogged(context.Background(), "u1", "act-1", SessionKindActivity, sessionStart)

	got, err := repo.Get(context.Background(), "u1", id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusCompleted {
		t.Errorf("status = %q want completed", got.Status)
	}
	if got.CompletedSessionID == nil || *got.CompletedSessionID != "act-1" {
		t.Errorf("completed_session_id = %v want act-1", got.CompletedSessionID)
	}
	if got.CompletedSessionKind == nil || *got.CompletedSessionKind != SessionKindActivity {
		t.Errorf("completed_session_kind = %v want activity", got.CompletedSessionKind)
	}

	// Deleting the session reverts the plan back to planned and clears the link.
	svc.OnSessionDeleted(context.Background(), "u1", "act-1", SessionKindActivity)

	got, err = repo.Get(context.Background(), "u1", id)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got.Status != StatusPlanned {
		t.Errorf("status after delete = %q want planned", got.Status)
	}
	if got.CompletedSessionID != nil {
		t.Errorf("completed_session_id after delete = %v want nil", got.CompletedSessionID)
	}
	if got.CompletedSessionKind != nil {
		t.Errorf("completed_session_kind after delete = %v want nil", got.CompletedSessionKind)
	}
}

func TestService_OnSessionLogged_NoCandidateIsNoOp(t *testing.T) {
	repo := NewMemoryRepository()
	const ny = "America/New_York"
	planStart := time.Date(2026, 6, 15, 17, 30, 0, 0, time.UTC)
	id := seedRunPlan(t, repo, "u1", planStart, ny)

	svc := NewService(repo)
	svc.SetCalendar(&fakeScheduler{})

	// Session on a different day (within the ±36h window but different NY day).
	sessionStart := time.Date(2026, 6, 16, 18, 0, 0, 0, time.UTC)
	svc.OnSessionLogged(context.Background(), "u1", "act-1", SessionKindActivity, sessionStart)

	got, err := repo.Get(context.Background(), "u1", id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusPlanned {
		t.Errorf("status = %q want planned (no-op)", got.Status)
	}
	if got.CompletedSessionID != nil {
		t.Errorf("completed_session_id = %v want nil", got.CompletedSessionID)
	}
}

func TestService_OnSessionDeleted_NoLinkIsNoOp(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo)
	svc.SetCalendar(&fakeScheduler{})
	// No plan links act-x; must not panic or error.
	svc.OnSessionDeleted(context.Background(), "u1", "act-x", SessionKindActivity)
}
