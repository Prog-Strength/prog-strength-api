package plannedworkout

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// fakeScheduler is a CalendarScheduler stub recording calls. It can either
// return a scripted error or run an onSchedule hook (e.g. to flip the plan's
// sync status the way the real service would) so the handler's re-read behavior
// is exercised.
type fakeScheduler struct {
	scheduleErr       error
	resyncErr         error
	rewriteErr        error
	onSchedule        func(userID, planID string)
	scheduleCall      int
	resyncCall        int
	deleteCall        int
	rewriteCall       int
	lastDetail        string
	lastRewritePlanID string
	lastRewriteActual string
}

func (f *fakeScheduler) Schedule(ctx context.Context, userID, planID, detailOverride string) error {
	f.scheduleCall++
	f.lastDetail = detailOverride
	if f.onSchedule != nil {
		f.onSchedule(userID, planID)
	}
	return f.scheduleErr
}

func (f *fakeScheduler) Resync(ctx context.Context, userID, planID string) error {
	f.resyncCall++
	return f.resyncErr
}

func (f *fakeScheduler) Delete(ctx context.Context, userID, planID string) error {
	f.deleteCall++
	return nil
}

func (f *fakeScheduler) RewriteCompleted(ctx context.Context, userID, planID, actualText string) error {
	f.rewriteCall++
	f.lastRewritePlanID = planID
	f.lastRewriteActual = actualText
	return f.rewriteErr
}

// doCal is like do but wires a CalendarScheduler into the handler.
func doCal(t *testing.T, repo Repository, userRepo user.Repository, sched CalendarScheduler, userID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(repo, userRepo)
	if sched != nil {
		h.SetCalendarSync(sched)
	}
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), userID))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// seedPlan creates a planned workout directly via the repo and returns its id.
func seedPlan(t *testing.T, repo Repository, userID string) string {
	t.Helper()
	name := "Push Day"
	pw := &PlannedWorkout{
		UserID:            userID,
		Name:              &name,
		ActivityKind:      ActivityKindLift,
		ScheduledStartUTC: mustTime(t, "2026-07-01T09:00:00Z"),
		ScheduledEndUTC:   mustTime(t, "2026-07-01T10:00:00Z"),
		Timezone:          "UTC",
		Status:            StatusPlanned,
	}
	if err := repo.Create(context.Background(), pw); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	return pw.ID
}

func TestSchedule_NoSchedulerReturns503(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/schedule", "{}")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503, body=%s", w.Code, w.Body.String())
	}
}

func TestResync_NoSchedulerReturns503(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	w := doCal(t, repo, nil, nil, "u1", "POST", "/planned-workouts/"+id+"/resync", "{}")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503, body=%s", w.Code, w.Body.String())
	}
}

func TestSchedule_HappyPathReflectsSynced(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	sched := &fakeScheduler{
		onSchedule: func(userID, planID string) {
			// Emulate the real service persisting a synced status + event id.
			eventID := "evt-1"
			_ = repo.SetGoogleSync(context.Background(), userID, planID, &eventID, SyncSynced, nil)
		},
	}
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/schedule", `{"detail_level":"full_agenda"}`)
	got := decodePlan(t, w, http.StatusOK)
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != "synced" {
		t.Errorf("sync status = %v want synced", got.GoogleSyncStatus)
	}
	if got.GoogleEventID == nil || *got.GoogleEventID != "evt-1" {
		t.Errorf("event id = %v want evt-1", got.GoogleEventID)
	}
	if sched.lastDetail != "full_agenda" {
		t.Errorf("detail override = %q want full_agenda", sched.lastDetail)
	}
}

func TestSchedule_BestEffortFailureStill200WithFailed(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	sched := &fakeScheduler{
		scheduleErr: errIns{}, // a generic write failure, not a connection error
		onSchedule: func(userID, planID string) {
			msg := "boom"
			_ = repo.SetGoogleSync(context.Background(), userID, planID, nil, SyncFailed, &msg)
		},
	}
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/schedule", "{}")
	got := decodePlan(t, w, http.StatusOK)
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != "failed" {
		t.Errorf("sync status = %v want failed", got.GoogleSyncStatus)
	}
}

func TestSchedule_NotConnectedMapsTo409(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	sched := &fakeScheduler{scheduleErr: ErrCalendarNotConnected}
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/schedule", "{}")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d want 409, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "calendar_not_connected") {
		t.Errorf("expected code calendar_not_connected, body=%s", w.Body.String())
	}
}

func TestSchedule_ReconnectNeededMapsTo409(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	sched := &fakeScheduler{scheduleErr: ErrCalendarReconnectNeeded}
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/schedule", "{}")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d want 409, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "calendar_reconnect_needed") {
		t.Errorf("expected code calendar_reconnect_needed, body=%s", w.Body.String())
	}
}

func TestSchedule_UnknownPlan404(t *testing.T) {
	repo := NewMemoryRepository()
	sched := &fakeScheduler{}
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/nope/schedule", "{}")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404", w.Code)
	}
	if sched.scheduleCall != 0 {
		t.Errorf("scheduler should not be called for unknown plan")
	}
}

func TestResync_HappyPath(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	eventID := "evt-1"
	_ = repo.SetGoogleSync(context.Background(), "u1", id, &eventID, SyncSynced, nil)
	sched := &fakeScheduler{}
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/"+id+"/resync", "")
	decodePlan(t, w, http.StatusOK)
	if sched.resyncCall != 1 {
		t.Errorf("resync called %d times want 1", sched.resyncCall)
	}
}

func TestCreate_CalendarSyncTriggersSchedule(t *testing.T) {
	repo := NewMemoryRepository()
	sched := &fakeScheduler{
		onSchedule: func(userID, planID string) {
			eventID := "evt-1"
			_ = repo.SetGoogleSync(context.Background(), userID, planID, &eventID, SyncSynced, nil)
		},
	}
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC","calendar_sync":true}`
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/", body)
	got := decodePlan(t, w, http.StatusCreated)
	if sched.scheduleCall != 1 {
		t.Errorf("schedule called %d times want 1", sched.scheduleCall)
	}
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != "synced" {
		t.Errorf("create response sync status = %v want synced", got.GoogleSyncStatus)
	}
}

func TestCreate_NoCalendarSyncSkipsSchedule(t *testing.T) {
	repo := NewMemoryRepository()
	sched := &fakeScheduler{}
	body := `{"scheduled_start":"2026-07-01T09:00:00Z","scheduled_end":"2026-07-01T10:00:00Z","timezone":"UTC"}`
	w := doCal(t, repo, nil, sched, "u1", "POST", "/planned-workouts/", body)
	decodePlan(t, w, http.StatusCreated)
	if sched.scheduleCall != 0 {
		t.Errorf("schedule should not be called without calendar_sync")
	}
}

func TestDelete_RemovesCalendarEvent(t *testing.T) {
	repo := NewMemoryRepository()
	id := seedPlan(t, repo, "u1")
	sched := &fakeScheduler{}
	w := doCal(t, repo, nil, sched, "u1", "DELETE", "/planned-workouts/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d want 200", w.Code)
	}
	if sched.deleteCall != 1 {
		t.Errorf("calendar delete called %d times want 1", sched.deleteCall)
	}
}

// errIns is a sentinel-free generic error used for best-effort failure tests.
type errIns struct{}

func (errIns) Error() string { return "insert failed" }
