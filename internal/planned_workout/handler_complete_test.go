package plannedworkout

import (
	"context"
	"net/http"
	"testing"
)

func TestComplete_Workout200(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")

	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
	if got.CompletedSessionID == nil || *got.CompletedSessionID != "sess-1" {
		t.Errorf("completed_session_id = %v want sess-1", got.CompletedSessionID)
	}
	if got.CompletedSessionKind == nil || *got.CompletedSessionKind != "workout" {
		t.Errorf("completed_session_kind = %v want workout", got.CompletedSessionKind)
	}
}

func TestComplete_Activity200(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")

	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"act-9","session_kind":"activity"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
	if got.CompletedSessionKind == nil || *got.CompletedSessionKind != "activity" {
		t.Errorf("completed_session_kind = %v want activity", got.CompletedSessionKind)
	}
}

func TestComplete_MissingSessionID400(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_kind":"workout"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestComplete_MissingSessionKind400(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestComplete_InvalidSessionKind400(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"bogus"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestComplete_CrossUser404(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "user-a")
	w := doCal(t, repo, nil, nil, "user-b", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestComplete_SyncedRewritesGoogleEvent(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	// Mark the plan as Google-synced so completion triggers a rewrite.
	eventID := "evt-1"
	if err := repo.SetGoogleSync(context.Background(), "u1", id, &eventID, SyncSynced, nil); err != nil {
		t.Fatalf("set google sync: %v", err)
	}
	sched := &fakeScheduler{}

	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
	if sched.rewriteCall != 1 {
		t.Fatalf("RewriteCompleted called %d times want 1", sched.rewriteCall)
	}
	if sched.lastRewritePlanID != id {
		t.Errorf("rewrite plan id = %q want %q", sched.lastRewritePlanID, id)
	}
}

func TestComplete_NotSyncedSkipsRewrite(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	sched := &fakeScheduler{}

	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	decodePlan(t, w, http.StatusOK)
	if sched.rewriteCall != 0 {
		t.Errorf("RewriteCompleted called %d times want 0 (plan not synced)", sched.rewriteCall)
	}
}

func TestComplete_RewriteErrorStill200(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	eventID := "evt-1"
	if err := repo.SetGoogleSync(context.Background(), "u1", id, &eventID, SyncSynced, nil); err != nil {
		t.Fatalf("set google sync: %v", err)
	}
	sched := &fakeScheduler{rewriteErr: errIns{}}

	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed (rewrite error must not fail request)", got.Status)
	}
	if sched.rewriteCall != 1 {
		t.Errorf("RewriteCompleted called %d times want 1", sched.rewriteCall)
	}
}
