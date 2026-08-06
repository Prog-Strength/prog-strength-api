package plannedworkout

import (
	"context"
	"net/http"
	"testing"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

func TestComplete_Workout200(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")

	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
	if got.CompletedSessionID == nil || *got.CompletedSessionID != "sess-1" {
		t.Errorf("completed_session_id = %v want sess-1", got.CompletedSessionID)
	}
}

func TestComplete_Activity200(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")

	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"act-9","session_kind":"activity"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
	if got.CompletedSessionID == nil || *got.CompletedSessionID != "act-9" {
		t.Errorf("completed_session_id = %v want act-9", got.CompletedSessionID)
	}
}

func TestComplete_MissingSessionID400(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_kind":"workout"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400, body=%s", w.Code, w.Body.String())
	}
}

// TestComplete_OmittedSessionKindAccepted proves session_kind is now optional:
// omitting it succeeds (the discriminator was dropped in stage 5).
func TestComplete_OmittedSessionKindAccepted(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
}

// TestComplete_PresentSessionKindIgnored proves a present session_kind (even a
// nonsense one) is accepted and ignored rather than rejected — kept for backward
// compat with deployed clients and MCP, which still send it.
func TestComplete_PresentSessionKindIgnored(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"bogus"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "completed" {
		t.Errorf("status = %q want completed", got.Status)
	}
}

func TestComplete_CrossUser404(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "user-a")
	w := doCal(t, repo, nil, nil, "user-b", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestComplete_SyncedRewritesGoogleEvent(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
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
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")
	sched := &fakeScheduler{}

	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/complete", `{"session_id":"sess-1","session_kind":"workout"}`)
	decodePlan(t, w, http.StatusOK)
	if sched.rewriteCall != 0 {
		t.Errorf("RewriteCompleted called %d times want 0 (plan not synced)", sched.rewriteCall)
	}
}

func TestComplete_RewriteErrorStill200(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
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
