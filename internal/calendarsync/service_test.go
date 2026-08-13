package calendarsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	plannedworkout "github.com/Prog-Strength/prog-strength-api/internal/planned_workout"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
	"github.com/Prog-Strength/prog-strength-api/internal/user"
)

// fakeClient is an in-memory CalendarClient recording calls and returning
// scripted errors so the service's branches are exercised without Google.
type fakeClient struct {
	insertID  string
	insertErr error
	patchErr  error
	deleteErr error

	inserts  int
	patches  int
	deletes  int
	lastEv   GoogleEvent
	lastEvID string
}

func (f *fakeClient) InsertEvent(ctx context.Context, accessToken, calendarID string, ev GoogleEvent) (string, error) {
	f.inserts++
	f.lastEv = ev
	if f.insertErr != nil {
		return "", f.insertErr
	}
	id := f.insertID
	if id == "" {
		id = "evt-new"
	}
	return id, nil
}

func (f *fakeClient) PatchEvent(ctx context.Context, accessToken, calendarID, eventID string, ev GoogleEvent) error {
	f.patches++
	f.lastEv = ev
	f.lastEvID = eventID
	return f.patchErr
}

func (f *fakeClient) DeleteEvent(ctx context.Context, accessToken, calendarID, eventID string) error {
	f.deletes++
	f.lastEvID = eventID
	return f.deleteErr
}

// ListEvents satisfies CalendarClient. These tests only exercise the write
// path, so the read direction stays empty.
func (f *fakeClient) ListEvents(ctx context.Context, accessToken, calendarID string, timeMin, timeMax time.Time, maxResults int) ([]ListedEvent, error) {
	return nil, nil
}

// fakeTokens always returns a fixed access token (or a scripted error).
type fakeTokens struct {
	err error
}

func (f fakeTokens) Token(ctx context.Context, userID, refreshToken string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "access-token", nil
}

const testUserID = "user-1"

// newTestService wires a Service over ephemeral SQLite repos + a real cipher
// with a connected user and a single seeded plan, returning the service, the
// plan id, and the repos for assertions. The connection, plan, and user repos
// share ONE database so the service's per-user reads (connection, plan,
// CalendarDefaultDetail) all resolve against the same testUserID rows.
func newTestService(t *testing.T, client CalendarClient, tokens tokenMinter) (*Service, string, calendarconn.Repository, plannedworkout.Repository) {
	t.Helper()

	db := dbtest.New(t)
	conns := calendarconn.NewSQLiteRepository(db)
	plans := plannedworkout.NewSQLiteRepository(db)
	users := user.NewSQLiteRepository(db)

	cipher, err := tokencrypt.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	enc, nonce, err := cipher.Encrypt([]byte("refresh-token"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := conns.Upsert(context.Background(), testUserID, enc, nonce, "primary", CalendarEventsScope, time.Now()); err != nil {
		t.Fatalf("Upsert conn: %v", err)
	}

	plan := samplePlan()
	plan.UserID = testUserID
	plan.ID = ""
	plan.ActivityKind = plannedworkout.ActivityKindLift
	plan.Status = plannedworkout.StatusPlanned
	plan.GoogleEventID = nil
	if err := plans.Create(context.Background(), plan); err != nil {
		t.Fatalf("Create plan: %v", err)
	}

	svc := NewService(conns, cipher, nil, client, plans, users, "https://app.example.com", func() time.Time { return time.Unix(2000, 0) })
	svc.conn.tokens = tokens // inject fake token minter directly
	return svc, plan.ID, conns, plans
}

func TestSchedule_InsertsAndStores(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1"}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})

	if err := svc.Schedule(context.Background(), testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if fc.inserts != 1 {
		t.Errorf("inserts = %d want 1", fc.inserts)
	}
	got, _ := plans.Get(context.Background(), testUserID, planID)
	if got.GoogleEventID == nil || *got.GoogleEventID != "evt-1" {
		t.Errorf("event id not stored: %v", got.GoogleEventID)
	}
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != plannedworkout.SyncSynced {
		t.Errorf("status = %v want synced", got.GoogleSyncStatus)
	}
}

func TestSchedule_SecondCallPatches(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1"}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})

	if err := svc.Schedule(context.Background(), testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule 1: %v", err)
	}
	if err := svc.Schedule(context.Background(), testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule 2: %v", err)
	}
	if fc.inserts != 1 || fc.patches != 1 {
		t.Errorf("inserts=%d patches=%d want 1/1 (idempotent patch)", fc.inserts, fc.patches)
	}
	if fc.lastEvID != "evt-1" {
		t.Errorf("patched event id = %q want evt-1", fc.lastEvID)
	}
	got, _ := plans.Get(context.Background(), testUserID, planID)
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != plannedworkout.SyncSynced {
		t.Errorf("status = %v want synced", got.GoogleSyncStatus)
	}
}

func TestSchedule_ClientFailurePersistsFailedAndPlanSurvives(t *testing.T) {
	fc := &fakeClient{insertErr: errors.New("boom")}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})

	err := svc.Schedule(context.Background(), testUserID, planID, "")
	if err == nil {
		t.Fatal("expected error from failed insert")
	}
	got, gErr := plans.Get(context.Background(), testUserID, planID)
	if gErr != nil {
		t.Fatalf("plan must survive a failed write: %v", gErr)
	}
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != plannedworkout.SyncFailed {
		t.Errorf("status = %v want failed", got.GoogleSyncStatus)
	}
	if got.LastSyncError == nil || *got.LastSyncError == "" {
		t.Errorf("expected last sync error recorded")
	}
	if got.GoogleEventID != nil {
		t.Errorf("no event id should be stored on a failed insert, got %v", got.GoogleEventID)
	}
}

func TestSchedule_EventGoneOnPatchReinserts(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1"}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})

	if err := svc.Schedule(context.Background(), testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule 1: %v", err)
	}
	// Next write patches; make the patch report the event is gone, then the
	// re-insert should mint a fresh id.
	fc.patchErr = ErrEventGone
	fc.insertID = "evt-2"
	if err := svc.Schedule(context.Background(), testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule 2: %v", err)
	}
	if fc.patches != 1 || fc.inserts != 2 {
		t.Errorf("patches=%d inserts=%d want 1/2", fc.patches, fc.inserts)
	}
	got, _ := plans.Get(context.Background(), testUserID, planID)
	if got.GoogleEventID == nil || *got.GoogleEventID != "evt-2" {
		t.Errorf("event id = %v want evt-2 after re-insert", got.GoogleEventID)
	}
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != plannedworkout.SyncSynced {
		t.Errorf("status = %v want synced", got.GoogleSyncStatus)
	}
}

func TestSchedule_TokenRejectedRevokesConnection(t *testing.T) {
	fc := &fakeClient{insertErr: ErrTokenRejected}
	svc, planID, conns, plans := newTestService(t, fc, fakeTokens{})

	err := svc.Schedule(context.Background(), testUserID, planID, "")
	if !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v want ErrReconnectNeeded", err)
	}
	conn, _ := conns.Get(context.Background(), testUserID)
	if conn.Status != calendarconn.StatusRevoked {
		t.Errorf("connection status = %v want revoked", conn.Status)
	}
	got, _ := plans.Get(context.Background(), testUserID, planID)
	if got.GoogleSyncStatus == nil || *got.GoogleSyncStatus != plannedworkout.SyncFailed {
		t.Errorf("plan status = %v want failed (resyncable)", got.GoogleSyncStatus)
	}
}

func TestSchedule_TokenMintRejectedRevokes(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1"}
	svc, planID, conns, _ := newTestService(t, fc, fakeTokens{err: errors.New("invalid_grant")})

	err := svc.Schedule(context.Background(), testUserID, planID, "")
	if !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v want ErrReconnectNeeded", err)
	}
	conn, _ := conns.Get(context.Background(), testUserID)
	if conn.Status != calendarconn.StatusRevoked {
		t.Errorf("connection status = %v want revoked", conn.Status)
	}
	if fc.inserts != 0 {
		t.Errorf("should not write event when token mint fails")
	}
}

func TestSchedule_NoConnection(t *testing.T) {
	fc := &fakeClient{}
	svc, planID, conns, _ := newTestService(t, fc, fakeTokens{})
	if err := conns.Delete(context.Background(), testUserID); err != nil {
		t.Fatalf("Delete conn: %v", err)
	}

	err := svc.Schedule(context.Background(), testUserID, planID, "")
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("err = %v want ErrNotConnected", err)
	}
	if fc.inserts != 0 {
		t.Errorf("should not write without a connection")
	}
}

func TestSchedule_RevokedConnection(t *testing.T) {
	fc := &fakeClient{}
	svc, planID, conns, _ := newTestService(t, fc, fakeTokens{})
	if err := conns.SetStatus(context.Background(), testUserID, calendarconn.StatusRevoked, time.Now()); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	err := svc.Schedule(context.Background(), testUserID, planID, "")
	if !errors.Is(err, ErrReconnectNeeded) {
		t.Errorf("err = %v want ErrReconnectNeeded", err)
	}
}

func TestDelete_RemovesEventAndClearsID(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1"}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})

	if err := svc.Schedule(context.Background(), testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := svc.Delete(context.Background(), testUserID, planID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fc.deletes != 1 || fc.lastEvID != "evt-1" {
		t.Errorf("deletes=%d lastID=%q want 1/evt-1", fc.deletes, fc.lastEvID)
	}
	got, _ := plans.Get(context.Background(), testUserID, planID)
	if got.GoogleEventID != nil {
		t.Errorf("event id should be cleared, got %v", got.GoogleEventID)
	}
}

func TestDelete_NoEventIsNoop(t *testing.T) {
	fc := &fakeClient{}
	svc, planID, _, _ := newTestService(t, fc, fakeTokens{})
	if err := svc.Delete(context.Background(), testUserID, planID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fc.deletes != 0 {
		t.Errorf("no event → no delete call, got %d", fc.deletes)
	}
}

func TestDelete_EventGoneIsIgnored(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1", deleteErr: ErrEventGone}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})
	if err := svc.Schedule(context.Background(), testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := svc.Delete(context.Background(), testUserID, planID); err != nil {
		t.Fatalf("Delete should ignore ErrEventGone: %v", err)
	}
	got, _ := plans.Get(context.Background(), testUserID, planID)
	if got.GoogleEventID != nil {
		t.Errorf("event id should be cleared even when gone, got %v", got.GoogleEventID)
	}
}

// Once a plan is completed and linked, the ACTIVITY owns its Google event —
// see ActivityService.SyncCompletedActivity. Re-scheduling the plan over that
// event would replace "what I actually did" with "what I intended to do",
// which is a strict loss of information on an entry the user already watched
// turn green. (This replaces TestRewriteCompleted_PatchesWithMarker: the
// plan service no longer renders completed sessions at all.)
func TestSchedule_NoOpsForAPlanHandedOverToItsActivity(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1"}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})
	ctx := context.Background()
	if err := svc.Schedule(ctx, testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := plans.SetCompletion(ctx, testUserID, planID, "act_1"); err != nil {
		t.Fatalf("SetCompletion: %v", err)
	}
	before := fc.patches

	if err := svc.Schedule(ctx, testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule after completion: %v", err)
	}
	if fc.patches != before {
		t.Errorf("patches = %d want %d — a handed-over event must not be rewritten", fc.patches, before)
	}
}

// Deleting a completed, linked plan must NOT delete the session's event: the
// activity still exists and still owns that entry. Our pointer is cleared;
// Google is left alone.
func TestDelete_HandedOverPlanClearsThePointerWithoutDeletingAtGoogle(t *testing.T) {
	fc := &fakeClient{insertID: "evt-1"}
	svc, planID, _, plans := newTestService(t, fc, fakeTokens{})
	ctx := context.Background()
	if err := svc.Schedule(ctx, testUserID, planID, ""); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := plans.SetCompletion(ctx, testUserID, planID, "act_1"); err != nil {
		t.Fatalf("SetCompletion: %v", err)
	}

	if err := svc.Delete(ctx, testUserID, planID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fc.deletes != 0 {
		t.Errorf("deletes = %d want 0 — the activity still owns that event", fc.deletes)
	}
	got, _ := plans.Get(ctx, testUserID, planID)
	if got.GoogleEventID != nil {
		t.Errorf("plan event id = %v want cleared", *got.GoogleEventID)
	}
}
