package plannedworkout

import (
	"context"
	"testing"
)

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
