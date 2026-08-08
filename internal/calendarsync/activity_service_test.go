package calendarsync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/activity"
	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	plannedworkout "github.com/Prog-Strength/prog-strength-api/internal/planned_workout"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

// ---- fakes -----------------------------------------------------------------

type fakeActivities struct {
	act     *activity.Activity
	efforts []activity.ActivityBestEffort
	err     error
}

func (f *fakeActivities) Get(ctx context.Context, userID, activityID string) (*activity.Activity, error) {
	if f.err != nil {
		return nil, f.err
	}
	cp := *f.act
	return &cp, nil
}

func (f *fakeActivities) BestEffortsForActivity(ctx context.Context, userID, activityID string) ([]activity.ActivityBestEffort, error) {
	return f.efforts, nil
}

// memSyncState is an in-memory ActivitySyncRepository.
type memSyncState struct {
	rows map[string]*ActivitySyncState
}

func newMemSyncState() *memSyncState {
	return &memSyncState{rows: map[string]*ActivitySyncState{}}
}

func (m *memSyncState) Get(ctx context.Context, userID, activityID string) (*ActivitySyncState, error) {
	if s, ok := m.rows[activityID]; ok {
		return s, nil
	}
	return nil, ErrSyncStateNotFound
}

func (m *memSyncState) MarkSynced(ctx context.Context, userID, activityID, eventID string, now time.Time) error {
	id := eventID
	m.rows[activityID] = &ActivitySyncState{
		ActivityID: activityID, UserID: userID, GoogleEventID: &id,
		Status: SyncSynced, Attempts: 0, UpdatedAt: now,
	}
	return nil
}

func (m *memSyncState) MarkFailed(ctx context.Context, userID, activityID string, eventID *string, cause string, now time.Time) error {
	prev := m.rows[activityID]
	attempts := 1
	keep := eventID
	if prev != nil {
		attempts = prev.Attempts + 1
		if keep == nil {
			keep = prev.GoogleEventID
		}
	}
	c := cause
	m.rows[activityID] = &ActivitySyncState{
		ActivityID: activityID, UserID: userID, GoogleEventID: keep,
		Status: SyncFailed, LastError: &c, Attempts: attempts, UpdatedAt: now,
	}
	return nil
}

func (m *memSyncState) Release(ctx context.Context, userID, activityID string, now time.Time) error {
	m.rows[activityID] = &ActivitySyncState{
		ActivityID: activityID, UserID: userID, Status: SyncPending, UpdatedAt: now,
	}
	return nil
}

func (m *memSyncState) PendingSince(ctx context.Context, maxAttempts, limit int) ([]PendingSync, error) {
	return nil, nil
}

// fakePlans serves the takeover lookup.
type fakePlans struct {
	plan        *plannedworkout.PlannedWorkout
	clearedPlan string
}

func (f *fakePlans) GetByCompletedSession(ctx context.Context, userID, sessionID string) (*plannedworkout.PlannedWorkout, error) {
	if f.plan == nil {
		return nil, errors.New("not found")
	}
	return f.plan, nil
}

func (f *fakePlans) SetGoogleSync(ctx context.Context, userID, id string, eventID *string, status plannedworkout.GoogleSyncStatus, lastErr *string) error {
	if eventID == nil {
		f.clearedPlan = id
	}
	return nil
}

type recordingObserver struct{ results []string }

func (r *recordingObserver) ObserveSync(activityType, trigger, result string) {
	r.results = append(r.results, result)
}

// ---- harness ---------------------------------------------------------------

var (
	connectedAt = time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	loggedAt    = connectedAt.Add(time.Hour)
)

type harness struct {
	svc    *ActivityService
	client *fakeClient
	state  *memSyncState
	plans  *fakePlans
	obs    *recordingObserver
}

// newActivityHarness wires an ActivityService over a real connection repo (so
// the connected_at cutoff is exercised against real stored state) and fakes
// for everything else.
func newActivityHarness(t *testing.T, act *activity.Activity, client *fakeClient) *harness {
	t.Helper()

	db := dbtest.New(t)
	conns := calendarconn.NewSQLiteRepository(db)
	users := user.NewSQLiteRepository(db)

	cipher, err := tokencrypt.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	enc, nonce, err := cipher.Encrypt([]byte("refresh-token"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := conns.Upsert(context.Background(), testUserID, enc, nonce, "primary", CalendarEventsScope, connectedAt); err != nil {
		t.Fatalf("Upsert conn: %v", err)
	}

	reg := activity.NewRegistry(activity.NewEnduranceDescriptor(activity.ActivityRunning, nil))
	state := newMemSyncState()
	plans := &fakePlans{}
	obs := &recordingObserver{}

	svc := NewActivityService(ActivityServiceDeps{
		Conns:       conns,
		Cipher:      cipher,
		Client:      client,
		Activities:  &fakeActivities{act: act},
		Registry:    reg,
		State:       state,
		Plans:       plans,
		Users:       users,
		AppLinkBase: "https://app.example.com",
		Now:         func() time.Time { return loggedAt },
		Observer:    obs,
	})
	svc.conn.tokens = fakeTokens{}

	return &harness{svc: svc, client: client, state: state, plans: plans, obs: obs}
}

func runActivity(createdAt time.Time) *activity.Activity {
	name := "Threshold Run"
	return &activity.Activity{
		ID:              "act_1",
		UserID:          testUserID,
		ActivityType:    activity.ActivityRunning,
		Name:            &name,
		StartTime:       connectedAt.Add(30 * time.Minute),
		DurationSeconds: 2472,
		DistanceMeters:  8368.6,
		CreatedAt:       createdAt,
	}
}

// ---- tests -----------------------------------------------------------------

func TestSyncActivity_InsertsAndRecordsState(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertID: "evt-1"})

	if err := h.svc.SyncActivity(context.Background(), testUserID, "act_1"); err != nil {
		t.Fatalf("SyncActivity: %v", err)
	}
	if h.client.inserts != 1 {
		t.Fatalf("inserts = %d, want 1", h.client.inserts)
	}
	st, err := h.state.Get(context.Background(), testUserID, "act_1")
	if err != nil {
		t.Fatalf("state Get: %v", err)
	}
	if st.Status != SyncSynced || st.GoogleEventID == nil || *st.GoogleEventID != "evt-1" {
		t.Fatalf("state = %+v, want synced against evt-1", st)
	}
}

func TestSyncActivity_EventBodyCarriesHeadlineAndDeepLink(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertID: "evt-1"})
	_ = h.svc.SyncActivity(context.Background(), testUserID, "act_1")

	ev := h.client.lastEv
	if ev.Summary != "✓ Threshold Run" {
		t.Fatalf("Summary = %q, want the completed marker + name", ev.Summary)
	}
	if !strings.Contains(ev.Description, "PROG STRENGTH · Run") {
		t.Fatalf("Description missing the branded header:\n%s", ev.Description)
	}
	if !strings.Contains(ev.Description, "5.2 mi") {
		t.Fatalf("Description missing the headline:\n%s", ev.Description)
	}
	if !strings.Contains(ev.Description, "https://app.example.com/activities/act_1") {
		t.Fatalf("Description missing the canonical deep link:\n%s", ev.Description)
	}
}

// The no-backfill cutoff, expressed end to end.
func TestSyncActivity_SkipsActivitiesLoggedBeforeConnecting(t *testing.T) {
	h := newActivityHarness(t, runActivity(connectedAt.Add(-24*time.Hour)), &fakeClient{})

	err := h.svc.SyncActivity(context.Background(), testUserID, "act_1")
	if !errors.Is(err, ErrSyncSkipped) {
		t.Fatalf("err = %v, want ErrSyncSkipped", err)
	}
	if h.client.inserts != 0 {
		t.Fatalf("inserts = %d, want 0 for a pre-connection activity", h.client.inserts)
	}
}

// A session performed before connecting but LOGGED after it must still sync —
// the cutoff is created_at, not start_time.
func TestSyncActivity_SyncsALateLoggedSessionFromBeforeConnecting(t *testing.T) {
	a := runActivity(loggedAt)
	a.StartTime = connectedAt.Add(-36 * time.Hour) // happened before connecting
	h := newActivityHarness(t, a, &fakeClient{insertID: "evt-1"})

	if err := h.svc.SyncActivity(context.Background(), testUserID, "act_1"); err != nil {
		t.Fatalf("SyncActivity: %v", err)
	}
	if h.client.inserts != 1 {
		t.Fatalf("inserts = %d, want 1", h.client.inserts)
	}
	if !h.client.lastEv.StartUTC.Equal(a.StartTime.UTC()) {
		t.Fatalf("event start = %v, want the historical start time %v", h.client.lastEv.StartUTC, a.StartTime)
	}
}

func TestSyncActivity_SecondSyncPatchesRatherThanDuplicating(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertID: "evt-1"})
	ctx := context.Background()

	_ = h.svc.SyncActivity(ctx, testUserID, "act_1")
	_ = h.svc.SyncActivity(ctx, testUserID, "act_1")

	if h.client.inserts != 1 {
		t.Fatalf("inserts = %d, want exactly 1", h.client.inserts)
	}
	if h.client.patches != 1 {
		t.Fatalf("patches = %d, want 1 on the second sync", h.client.patches)
	}
}

// The takeover: an activity that completed a plan adopts the plan's event
// instead of creating a second one for the same session.
func TestSyncActivity_AdoptsThePlannedWorkoutsEvent(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{})
	planEventID := "evt-from-plan"
	h.plans.plan = &plannedworkout.PlannedWorkout{ID: "plan_1", GoogleEventID: &planEventID}

	if err := h.svc.SyncActivity(context.Background(), testUserID, "act_1"); err != nil {
		t.Fatalf("SyncActivity: %v", err)
	}
	if h.client.inserts != 0 {
		t.Fatalf("inserts = %d, want 0 — the plan's event should be adopted", h.client.inserts)
	}
	if h.client.patches != 1 || h.client.lastEvID != planEventID {
		t.Fatalf("patched %q (%d times), want a patch of %q", h.client.lastEvID, h.client.patches, planEventID)
	}
}

// If the user deleted the event in Google, the next sync must converge by
// re-inserting rather than failing forever.
func TestSyncActivity_ReinsertsWhenGoogleReportsTheEventGone(t *testing.T) {
	client := &fakeClient{insertID: "evt-1"}
	h := newActivityHarness(t, runActivity(loggedAt), client)
	ctx := context.Background()

	_ = h.svc.SyncActivity(ctx, testUserID, "act_1")
	client.patchErr = ErrEventGone
	client.insertID = "evt-2"

	if err := h.svc.SyncActivity(ctx, testUserID, "act_1"); err != nil {
		t.Fatalf("SyncActivity after event gone: %v", err)
	}
	st, _ := h.state.Get(ctx, testUserID, "act_1")
	if st.GoogleEventID == nil || *st.GoogleEventID != "evt-2" {
		t.Fatalf("event id = %v, want the freshly inserted evt-2", st.GoogleEventID)
	}
}

func TestSyncActivity_FailedWriteRecordsResyncableState(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertErr: errors.New("google 500")})

	err := h.svc.SyncActivity(context.Background(), testUserID, "act_1")
	if err == nil {
		t.Fatal("SyncActivity err = nil, want the write failure surfaced")
	}
	st, _ := h.state.Get(context.Background(), testUserID, "act_1")
	if st.Status != SyncFailed || st.Attempts != 1 {
		t.Fatalf("state = %+v, want failed with 1 attempt", st)
	}
}

// A rejected token must flip the connection so the UI prompts re-consent.
func TestSyncActivity_TokenRejectionRevokesTheConnection(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertErr: ErrTokenRejected})

	err := h.svc.SyncActivity(context.Background(), testUserID, "act_1")
	if !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v, want ErrReconnectNeeded", err)
	}
	if got := h.obs.results[0]; got != "reconnect_needed" {
		t.Fatalf("observed %q, want reconnect_needed", got)
	}
}

func TestSyncActivity_ObservesOutcomes(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertID: "evt-1"})
	_ = h.svc.SyncActivity(context.Background(), testUserID, "act_1")

	if len(h.obs.results) != 1 || h.obs.results[0] != "synced" {
		t.Fatalf("observed %v, want one synced", h.obs.results)
	}
}

// A skip must be observed as a skip, never as a success — advancing the
// liveness signal for a write that never happened is the WHOOP failure mode.
func TestSyncActivity_SkipIsObservedDistinctlyFromSuccess(t *testing.T) {
	h := newActivityHarness(t, runActivity(connectedAt.Add(-24*time.Hour)), &fakeClient{})
	_ = h.svc.SyncActivity(context.Background(), testUserID, "act_1")

	if len(h.obs.results) != 1 || h.obs.results[0] != "skipped" {
		t.Fatalf("observed %v, want one skipped", h.obs.results)
	}
}

func TestDeleteActivityEvent_DeletesAndReleases(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertID: "evt-1"})
	ctx := context.Background()
	_ = h.svc.SyncActivity(ctx, testUserID, "act_1")

	if err := h.svc.DeleteActivityEvent(ctx, testUserID, "act_1"); err != nil {
		t.Fatalf("DeleteActivityEvent: %v", err)
	}
	if h.client.deletes != 1 {
		t.Fatalf("deletes = %d, want 1", h.client.deletes)
	}
	st, _ := h.state.Get(ctx, testUserID, "act_1")
	if st.GoogleEventID != nil {
		t.Fatalf("event id = %v, want released", *st.GoogleEventID)
	}
}

// Deleting an activity that never synced is a no-op, not an error.
func TestDeleteActivityEvent_NoStateIsANoOp(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{})

	if err := h.svc.DeleteActivityEvent(context.Background(), testUserID, "act_1"); err != nil {
		t.Fatalf("DeleteActivityEvent: %v", err)
	}
	if h.client.deletes != 0 {
		t.Fatalf("deletes = %d, want 0", h.client.deletes)
	}
}

// An event already gone at Google still leaves us in the correct end state.
func TestDeleteActivityEvent_ToleratesAnAlreadyGoneEvent(t *testing.T) {
	client := &fakeClient{insertID: "evt-1"}
	h := newActivityHarness(t, runActivity(loggedAt), client)
	ctx := context.Background()
	_ = h.svc.SyncActivity(ctx, testUserID, "act_1")
	client.deleteErr = ErrEventGone

	if err := h.svc.DeleteActivityEvent(ctx, testUserID, "act_1"); err != nil {
		t.Fatalf("DeleteActivityEvent: %v", err)
	}
	st, _ := h.state.Get(ctx, testUserID, "act_1")
	if st.GoogleEventID != nil {
		t.Fatalf("event id = %v, want released", *st.GoogleEventID)
	}
}

// Completion hands the event to the activity and clears the plan's pointer,
// so a later plan resync cannot overwrite the completed body.
func TestSyncCompletedActivity_ClearsThePlansEventPointer(t *testing.T) {
	h := newActivityHarness(t, runActivity(loggedAt), &fakeClient{insertID: "evt-1"})
	planEventID := "evt-from-plan"
	h.plans.plan = &plannedworkout.PlannedWorkout{ID: "plan_1", GoogleEventID: &planEventID}

	if err := h.svc.SyncCompletedActivity(context.Background(), testUserID, "plan_1", "act_1"); err != nil {
		t.Fatalf("SyncCompletedActivity: %v", err)
	}
	if h.plans.clearedPlan != "plan_1" {
		t.Fatalf("clearedPlan = %q, want plan_1", h.plans.clearedPlan)
	}
}

// A zero-duration manual log must not become an invisible instantaneous tick.
func TestSyncActivity_ZeroDurationGetsAMinimumWindow(t *testing.T) {
	a := runActivity(loggedAt)
	a.DurationSeconds = 0
	h := newActivityHarness(t, a, &fakeClient{insertID: "evt-1"})

	_ = h.svc.SyncActivity(context.Background(), testUserID, "act_1")
	ev := h.client.lastEv
	if got := ev.EndUTC.Sub(ev.StartUTC); got != minEventDuration {
		t.Fatalf("event duration = %v, want the %v floor", got, minEventDuration)
	}
}
