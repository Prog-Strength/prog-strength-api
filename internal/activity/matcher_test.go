package activity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

// fakePlanMatcher records the OnSessionLogged refs and OnSessionDeleted ids it
// receives so handler tests can assert the ingest/delete hooks fired.
type fakePlanMatcher struct {
	logged  []loggedCall
	deleted []deletedCall
}

type loggedCall struct {
	userID string
	ref    SessionRef
}

type deletedCall struct {
	userID    string
	sessionID string
}

func (f *fakePlanMatcher) OnSessionLogged(_ context.Context, userID string, ref SessionRef) {
	f.logged = append(f.logged, loggedCall{userID: userID, ref: ref})
}

func (f *fakePlanMatcher) OnSessionDeleted(_ context.Context, userID, sessionID string) {
	f.deleted = append(f.deleted, deletedCall{userID: userID, sessionID: sessionID})
}

var _ PlanMatcher = (*fakePlanMatcher)(nil)

// TestPlanMatcher_RunningUploadFiresOnSessionLogged proves a running TCX import
// calls OnSessionLogged exactly once with the new activity id and its start
// time.
func TestPlanMatcher_RunningUploadFiresOnSessionLogged(t *testing.T) {
	h, _, repo := newTestHandler()
	fake := &fakePlanMatcher{}
	h.SetPlanMatcher(fake)

	w := doImport(t, h, readFixture(t, "typical_5k.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	if len(fake.logged) != 1 {
		t.Fatalf("OnSessionLogged calls = %d, want 1", len(fake.logged))
	}

	// Recover the persisted activity to compare id + start time against the
	// ref the matcher received.
	got, err := repo.List(context.Background(), testUserID, 10, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("persisted activities = %d, want 1", len(got))
	}
	act := got[0]

	call := fake.logged[0]
	if call.userID != testUserID {
		t.Errorf("logged userID = %q, want %q", call.userID, testUserID)
	}
	if call.ref.SessionID != act.ID {
		t.Errorf("logged SessionID = %q, want %q", call.ref.SessionID, act.ID)
	}
	if !call.ref.StartUTC.Equal(act.StartTime) {
		t.Errorf("logged StartUTC = %v, want %v", call.ref.StartUTC, act.StartTime)
	}
}

// TestPlanMatcher_NonRunningUploadDoesNotFire proves a non-running upload (a
// cycling TCX, which the ingest pipeline classifies as ActivityCycling) does
// NOT call OnSessionLogged — only running activities reconcile against a plan.
func TestPlanMatcher_NonRunningUploadDoesNotFire(t *testing.T) {
	h, _, _ := newTestHandler()
	fake := &fakePlanMatcher{}
	h.SetPlanMatcher(fake)

	w := doImport(t, h, readFixture(t, "biking.tcx"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	if len(fake.logged) != 0 {
		t.Fatalf("OnSessionLogged calls = %d, want 0 for non-running upload", len(fake.logged))
	}
}

// TestPlanMatcher_DeleteFiresOnSessionDeleted proves deleting an activity calls
// OnSessionDeleted with that activity id.
func TestPlanMatcher_DeleteFiresOnSessionDeleted(t *testing.T) {
	h, _, repo := newTestHandler()
	fake := &fakePlanMatcher{}
	h.SetPlanMatcher(fake)

	a := newActivity(testUserID, IngestManualTCX, "x", time.Now().UTC(), 1000, 300)
	if err := repo.Create(context.Background(), a, []byte("<tcx/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	del := httptest.NewRequest("DELETE", "/activities/"+a.ID, nil)
	del = withParam(del.WithContext(authctx.WithUserID(del.Context(), testUserID)), "id", a.ID)
	wd := httptest.NewRecorder()
	h.delete(wd, del)
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", wd.Code)
	}

	if len(fake.deleted) != 1 {
		t.Fatalf("OnSessionDeleted calls = %d, want 1", len(fake.deleted))
	}
	call := fake.deleted[0]
	if call.userID != testUserID {
		t.Errorf("deleted userID = %q, want %q", call.userID, testUserID)
	}
	if call.sessionID != a.ID {
		t.Errorf("deleted sessionID = %q, want %q", call.sessionID, a.ID)
	}
}

// TestPlanMatcher_NilIsNoOp proves the nil-safe path: an upload and a delete
// with no matcher set must not panic (existing tests exercise this implicitly;
// this asserts it explicitly).
func TestPlanMatcher_NilIsNoOp(t *testing.T) {
	h, _, repo := newTestHandler()
	// no SetPlanMatcher call — planMatcher stays nil.

	if w := doImport(t, h, readFixture(t, "typical_5k.tcx")); w.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	got, err := repo.List(context.Background(), testUserID, 10, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("persisted activities = %d, want 1", len(got))
	}

	del := httptest.NewRequest("DELETE", "/activities/"+got[0].ID, nil)
	del = withParam(del.WithContext(authctx.WithUserID(del.Context(), testUserID)), "id", got[0].ID)
	wd := httptest.NewRecorder()
	h.delete(wd, del)
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", wd.Code)
	}
}
