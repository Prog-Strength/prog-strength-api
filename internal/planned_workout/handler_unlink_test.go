package plannedworkout

import (
	"context"
	"net/http"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
)

// completePlan marks a seeded plan completed via the complete endpoint so the
// unlink/by-session tests start from a linked plan.
func completePlan(t *testing.T, repo Repository, userID, id, sessionID, kind string) {
	t.Helper()
	w := doCal(t, repo, nil, nil, userID, "POST", "/planned-workouts/"+id+"/complete",
		`{"session_id":"`+sessionID+`","session_kind":"`+kind+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("seed completion: status %d body=%s", w.Code, w.Body.String())
	}
}

func TestUnlink_CompletedPlan200(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")
	completePlan(t, repo, "u1", id, "sess-1", "workout")

	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/unlink", "")
	got := decodePlan(t, w, http.StatusOK)
	if got.Status != "planned" {
		t.Errorf("status = %q want planned", got.Status)
	}
	if got.CompletedSessionID != nil {
		t.Errorf("completed_session_id = %v want nil", got.CompletedSessionID)
	}
	if got.CompletedSessionKind != nil {
		t.Errorf("completed_session_kind = %v want nil", got.CompletedSessionKind)
	}
}

func TestUnlink_SyncedResyncs(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")
	completePlan(t, repo, "u1", id, "sess-1", "workout")
	eventID := "evt-1"
	if err := repo.SetGoogleSync(context.Background(), "u1", id, &eventID, SyncSynced, nil); err != nil {
		t.Fatalf("set google sync: %v", err)
	}
	sched := &fakeScheduler{}

	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/unlink", "")
	decodePlan(t, w, http.StatusOK)
	if sched.resyncCall != 1 {
		t.Errorf("Resync called %d times want 1", sched.resyncCall)
	}
}

func TestUnlink_UnknownID404(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/nope/unlink", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestBySession_Found200(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	id := seedPlan(t, repo, "u1")
	completePlan(t, repo, "u1", id, "sess-1", "workout")

	w := doCal(t, repo, nil, nil, "u1", "GET", "/planned-workouts/by-session?session_id=sess-1&session_kind=workout", "")
	got := decodePlan(t, w, http.StatusOK)
	if got.ID != id {
		t.Errorf("id = %q want %q", got.ID, id)
	}
	if got.CompletedSessionID == nil || *got.CompletedSessionID != "sess-1" {
		t.Errorf("completed_session_id = %v want sess-1", got.CompletedSessionID)
	}
}

func TestBySession_MissingParams400(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := doCal(t, repo, nil, nil, "u1", "GET", "/planned-workouts/by-session?session_id=sess-1", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestBySession_InvalidKind400(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := doCal(t, repo, nil, nil, "u1", "GET", "/planned-workouts/by-session?session_id=sess-1&session_kind=bogus", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestBySession_NoMatch404(t *testing.T) {
	repo := NewSQLiteRepository(dbtest.New(t))
	w := doCal(t, repo, nil, nil, "u1", "GET", "/planned-workouts/by-session?session_id=ghost&session_kind=workout", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404, body=%s", w.Code, w.Body.String())
	}
}
