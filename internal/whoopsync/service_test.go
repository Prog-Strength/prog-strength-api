package whoopsync

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/tokencrypt"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

func fptr(f float64) *float64 { return &f }

// --- deriveDate -------------------------------------------------------------

func TestDeriveDate(t *testing.T) {
	cases := []struct {
		name       string
		start, off string
		want       string
		wantErr    bool
	}{
		{"midday negative offset", "2026-01-15T12:00:00Z", "-08:00", "2026-01-15", false},
		{"early UTC rolls back a day", "2026-01-15T02:00:00Z", "-08:00", "2026-01-14", false},
		{"late UTC rolls forward a day", "2026-01-15T20:00:00Z", "+11:00", "2026-01-16", false},
		// DST-adjacent instant: the raw offset is authoritative (no IANA lookup),
		// so a US "spring forward" weekend still formats straight from -07:00.
		{"dst adjacent uses raw offset", "2026-03-08T05:30:00Z", "-07:00", "2026-03-07", false},
		{"exact midnight boundary negative", "2026-01-15T08:00:00Z", "-08:00", "2026-01-15", false},
		{"bad start", "not-a-time", "-08:00", "", true},
		{"bad offset", "2026-01-15T12:00:00Z", "-8:00", "", true},
		{"empty offset", "2026-01-15T12:00:00Z", "", "", true},
		{"offset out of range", "2026-01-15T12:00:00Z", "+25:00", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveDate(tc.start, tc.off)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("deriveDate(%q,%q) = %q, want %q", tc.start, tc.off, got, tc.want)
			}
		})
	}
}

// --- fakes ------------------------------------------------------------------

// fakeAPI records what it was asked for and returns canned data. It also stamps
// callOrder so tests can assert the refresher ran before any API call.
type fakeAPI struct {
	recoveries []Recovery
	cycles     []Cycle
	recErr     error // if set, Recoveries returns it (simulates a WHOOP fetch failure)
	cycErr     error // if set, Cycles returns it
	lastToken  string
	calls      int32
	orderTick  *int32 // shared monotonic counter
	firstOrder int32  // order tick of the first API call
}

func (f *fakeAPI) Recoveries(_ context.Context, accessToken string, _, _ time.Time, _ int) ([]Recovery, error) {
	if atomic.AddInt32(&f.calls, 1) == 1 && f.orderTick != nil {
		f.firstOrder = atomic.AddInt32(f.orderTick, 1)
	}
	f.lastToken = accessToken
	if f.recErr != nil {
		return nil, f.recErr
	}
	return f.recoveries, nil
}

func (f *fakeAPI) Cycles(_ context.Context, accessToken string, _, _ time.Time, _ int) ([]Cycle, error) {
	f.lastToken = accessToken
	if f.cycErr != nil {
		return nil, f.cycErr
	}
	return f.cycles, nil
}

// fakeRefresher returns a canned token pair (or error). It stamps refreshOrder.
type fakeRefresher struct {
	tokens        *Tokens
	err           error
	calls         int32
	orderTick     *int32
	refreshOrder  int32
	seenRefreshTk string
}

func (f *fakeRefresher) Refresh(_ context.Context, _ *http.Client, refreshToken string) (*Tokens, error) {
	atomic.AddInt32(&f.calls, 1)
	f.seenRefreshTk = refreshToken
	if f.orderTick != nil {
		f.refreshOrder = atomic.AddInt32(f.orderTick, 1)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.tokens, nil
}

// --- harness ----------------------------------------------------------------

func newCipher(t *testing.T) *tokencrypt.Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := tokencrypt.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return c
}

// seedConnection upserts a connected connection for userID with the given
// access/refresh plaintext and token expiry.
func seedConnection(t *testing.T, conns whoopconn.Repository, cipher *tokencrypt.Cipher, userID, access, refresh string, expiresAt, now time.Time) {
	t.Helper()
	ctx := context.Background()
	aEnc, aNonce, err := cipher.Encrypt([]byte(access))
	if err != nil {
		t.Fatalf("encrypt access: %v", err)
	}
	rEnc, rNonce, err := cipher.Encrypt([]byte(refresh))
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	bundle := whoopconn.TokenBundle{
		AccessTokenEnc:    aEnc,
		AccessTokenNonce:  aNonce,
		RefreshTokenEnc:   rEnc,
		RefreshTokenNonce: rNonce,
		ExpiresAt:         expiresAt,
	}
	if err := conns.Upsert(ctx, userID, 42, bundle, ScopeString, now); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func newRepos(t *testing.T) (whoopconn.Repository, whooprecovery.Repository) {
	t.Helper()
	database := dbtest.New(t)
	return whoopconn.NewSQLiteRepository(database), whooprecovery.NewSQLiteRepository(database)
}

// --- SyncWindow: SCORED-only, missing-cycle skip ----------------------------

func TestSyncWindow_UpsertsOnlyScoredSkipsMissingCycle(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)

	// token expires well in the future → no refresh path exercised here.
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{
		cycles: []Cycle{
			{ID: 1, Start: "2026-01-15T12:00:00Z", TimezoneOffset: "-08:00"}, // -> 2026-01-15
			{ID: 2, Start: "2026-01-16T02:00:00Z", TimezoneOffset: "-08:00"}, // -> 2026-01-15 (rolls back)
			{ID: 3, Start: "2026-01-17T12:00:00Z", TimezoneOffset: "-08:00"}, // -> 2026-01-17
		},
		recoveries: []Recovery{
			{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-15T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(72), RestingHeartRate: fptr(55), HRVRmssdMilli: fptr(40)}},
			{CycleID: 2, SleepID: "s2", ScoreState: "PENDING"},
			{CycleID: 3, SleepID: "s3", ScoreState: "UNSCORABLE"},
			{CycleID: 99, SleepID: "s4", CreatedAt: "2026-01-18T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(80)}}, // cycle absent → skip
		},
	}
	refr := &fakeRefresher{}

	svc := NewService(conns, rec, cipher, api, refr, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}
	if refr.calls != 0 {
		t.Fatalf("refresher should not have been called, got %d", refr.calls)
	}
	if api.lastToken != "access-tok" {
		t.Fatalf("api used token %q, want access-tok", api.lastToken)
	}

	got, err := rec.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 SCORED row, got %d: %+v", len(got), got)
	}
	if got[0].Date != "2026-01-15" || got[0].CycleID != 1 || got[0].SleepID != "s1" {
		t.Fatalf("unexpected row: %+v", got[0])
	}
	if got[0].RecoveryScore == nil || *got[0].RecoveryScore != 72 ||
		got[0].RestingHeartRate == nil || *got[0].RestingHeartRate != 55 ||
		got[0].HRVRmssdMilli == nil || *got[0].HRVRmssdMilli != 40 {
		t.Fatalf("nullable score fields not copied: %+v", got[0])
	}
}

// --- Idempotency ------------------------------------------------------------

func TestSyncWindow_Idempotent(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{
		cycles:     []Cycle{{ID: 1, Start: "2026-01-15T12:00:00Z", TimezoneOffset: "-08:00"}},
		recoveries: []Recovery{{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-15T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(72)}}},
	}
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	for i := 0; i < 5; i++ {
		if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
			t.Fatalf("SyncWindow run %d: %v", i, err)
		}
	}
	got, err := rec.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 row after 5 syncs, got %d", len(got))
	}
}

// --- Refresh ordering + persistence -----------------------------------------

func TestSyncWindow_RefreshPersistsNewTokensBeforeUse(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)

	// token already expired → refresh path.
	seedConnection(t, conns, cipher, "u1", "old-access", "old-refresh", now.Add(-time.Minute), now)

	var tick int32
	newExp := now.Add(time.Hour)
	refr := &fakeRefresher{
		tokens:    &Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresAt: newExp},
		orderTick: &tick,
	}
	api := &fakeAPI{
		cycles:     []Cycle{{ID: 1, Start: "2026-01-15T12:00:00Z", TimezoneOffset: "-08:00"}},
		recoveries: []Recovery{{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-15T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(90)}}},
		orderTick:  &tick,
	}

	svc := NewService(conns, rec, cipher, api, refr, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}

	if refr.calls != 1 {
		t.Fatalf("refresher calls = %d, want 1", refr.calls)
	}
	if refr.seenRefreshTk != "old-refresh" {
		t.Fatalf("refresher saw refresh token %q, want old-refresh", refr.seenRefreshTk)
	}
	// Refresh ran before any API call consumed the token.
	if refr.refreshOrder == 0 || api.firstOrder == 0 || refr.refreshOrder >= api.firstOrder {
		t.Fatalf("expected refresh (order %d) before first api call (order %d)", refr.refreshOrder, api.firstOrder)
	}
	// The API was handed the NEW access token.
	if api.lastToken != "new-access" {
		t.Fatalf("api used token %q, want new-access", api.lastToken)
	}

	// New tokens were persisted (decrypt back to the new values).
	bundle, err := conns.GetTokens(ctx, "u1")
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	gotAccess, err := cipher.Decrypt(bundle.AccessTokenEnc, bundle.AccessTokenNonce)
	if err != nil {
		t.Fatalf("decrypt access: %v", err)
	}
	gotRefresh, err := cipher.Decrypt(bundle.RefreshTokenEnc, bundle.RefreshTokenNonce)
	if err != nil {
		t.Fatalf("decrypt refresh: %v", err)
	}
	if string(gotAccess) != "new-access" || string(gotRefresh) != "new-refresh" {
		t.Fatalf("persisted tokens = (%q,%q), want (new-access,new-refresh)", gotAccess, gotRefresh)
	}
	if !bundle.ExpiresAt.Equal(newExp) {
		t.Fatalf("persisted expiry = %v, want %v", bundle.ExpiresAt, newExp)
	}
}

func TestSyncWindow_InvalidGrantSetsErrorAndReconnect(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "old-access", "old-refresh", now.Add(-time.Minute), now)

	refr := &fakeRefresher{err: ErrInvalidGrant}
	api := &fakeAPI{}
	svc := NewService(conns, rec, cipher, api, refr, http.DefaultClient, func() time.Time { return now })

	err := svc.SyncWindow(ctx, "u1", 10)
	if !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v, want ErrReconnectNeeded", err)
	}
	if api.calls != 0 {
		t.Fatalf("api should not be called after invalid grant, got %d", api.calls)
	}
	conn, err := conns.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if conn.Status != whoopconn.StatusError {
		t.Fatalf("status = %q, want error", conn.Status)
	}
}

func TestSyncWindow_NotConnectedReconnectNeeded(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access", "refresh", now.Add(time.Hour), now)

	// Flip to error (not connected).
	if err := conns.SetStatus(ctx, "u1", whoopconn.StatusError, now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	api := &fakeAPI{}
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v, want ErrReconnectNeeded", err)
	}
	if api.calls != 0 {
		t.Fatalf("api should not be called when not connected, got %d", api.calls)
	}
}

func TestSyncWindow_NoConnectionReconnectNeeded(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	svc := NewService(conns, rec, cipher, &fakeAPI{}, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "ghost", 10); !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v, want ErrReconnectNeeded", err)
	}
}

// --- Backfill wiring --------------------------------------------------------

func TestBackfill_SyncsWiderWindow(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access", "refresh", now.Add(time.Hour), now)

	api := &fakeAPI{
		cycles:     []Cycle{{ID: 1, Start: "2026-01-02T12:00:00Z", TimezoneOffset: "+00:00"}},
		recoveries: []Recovery{{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-02T07:10:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(65)}}},
	}
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.Backfill(ctx, "u1"); err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	got, err := rec.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Date != "2026-01-02" {
		t.Fatalf("backfill did not upsert expected row: %+v", got)
	}
}

// TestSyncWindow_DatesByScoredAtNotCycleStart pins the wake-day semantics:
// WHOOP cycles run sleep-onset → sleep-onset, so a recovery's cycle STARTS at
// the previous evening's bedtime. Dating by cycle.start pinned every recovery
// to the day before the user woke up with it — "today" never had data (the
// v0.79.x dashboard bug). The recovery scored on the morning of Jan 16 (cycle
// began 22:45 local Jan 15) must land on Jan 16.
func TestSyncWindow_DatesByScoredAtNotCycleStart(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 16, 20, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{
		// Bedtime 22:45 local Jan 15 (-08:00) = 06:45Z Jan 16.
		cycles: []Cycle{{ID: 7, Start: "2026-01-16T06:45:00Z", TimezoneOffset: "-08:00"}},
		// Scored 07:05 local Jan 16 = 15:05Z.
		recoveries: []Recovery{{CycleID: 7, SleepID: "s7", CreatedAt: "2026-01-16T15:05:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RestingHeartRate: fptr(52)}}},
	}
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}
	got, err := rec.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].Date != "2026-01-16" {
		t.Fatalf("date = %q (cycle-start day?), want 2026-01-16 (the day the user woke up with this recovery)", got[0].Date)
	}
}

// --- SyncSince (admin resync) -----------------------------------------------

// TestSyncSince_LabelsAdminResync pins the deliberate metric label: an operator
// resync must increment {admin_resync, ok}, NOT {window, ok}, so it does not
// mask the dead-ingestion alert that watches organic window syncs.
func TestSyncSince_LabelsAdminResync(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{
		cycles: []Cycle{
			{ID: 1, Start: "2026-01-15T12:00:00Z", TimezoneOffset: "-08:00"},
			{ID: 2, Start: "2026-01-16T12:00:00Z", TimezoneOffset: "-08:00"},
		},
		recoveries: []Recovery{
			{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-15T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(72)}},
			{CycleID: 2, SleepID: "s2", CreatedAt: "2026-01-16T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(80)}},
		},
	}
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	adminBefore := testutil.ToFloat64(syncsTotal.WithLabelValues("admin_resync", "ok"))
	windowBefore := testutil.ToFloat64(syncsTotal.WithLabelValues("window", "ok"))

	res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
	if err != nil {
		t.Fatalf("SyncSince: %v", err)
	}
	if res.Upserted != 2 {
		t.Fatalf("Upserted = %d, want 2 (result: %+v)", res.Upserted, res)
	}

	if got := testutil.ToFloat64(syncsTotal.WithLabelValues("admin_resync", "ok")) - adminBefore; got != 1 {
		t.Fatalf("{admin_resync,ok} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(syncsTotal.WithLabelValues("window", "ok")) - windowBefore; got != 0 {
		t.Fatalf("{window,ok} delta = %v, want 0 (admin resync must not touch the window liveness counter)", got)
	}
}

// syncKindFixture is the two-scored-recoveries API fake shared by the
// last_window_sync_at tests below; each asserts which sync kinds are allowed to
// advance the durable liveness timestamp.
func syncKindFixture() *fakeAPI {
	return &fakeAPI{
		cycles: []Cycle{
			{ID: 1, Start: "2026-01-15T12:00:00Z", TimezoneOffset: "-08:00"},
			{ID: 2, Start: "2026-01-16T12:00:00Z", TimezoneOffset: "-08:00"},
		},
		recoveries: []Recovery{
			{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-15T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(72)}},
			{CycleID: 2, SleepID: "s2", CreatedAt: "2026-01-16T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(80)}},
		},
	}
}

// TestSyncWindow_RecordsLastWindowSyncAt pins the durable half of the ingestion
// liveness signal. The in-process counter cannot carry this: the API restarts
// about as often as WHOOP nudges us, so increase() over the counter reads 0
// even when syncs are landing (see migration 052). The alert therefore reads a
// persisted timestamp, and only a real webhook-driven sync may advance it.
func TestSyncWindow_RecordsLastWindowSyncAt(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	connectedAt := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	syncedAt := connectedAt.Add(30 * time.Hour)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", syncedAt.Add(time.Hour), connectedAt)

	svc := NewService(conns, rec, cipher, syncKindFixture(), &fakeRefresher{}, http.DefaultClient, func() time.Time { return syncedAt })
	if err := svc.SyncWindow(ctx, "u1", 25); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}

	got, err := conns.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastWindowSyncAt == nil || !got.LastWindowSyncAt.Equal(syncedAt) {
		t.Fatalf("last_window_sync_at = %v, want %v", got.LastWindowSyncAt, syncedAt)
	}
}

// TestSyncWindow_FailedSyncDoesNotRecordLastWindowSyncAt pins that the
// timestamp tracks SUCCESS, not attempts. A sync that dies upstream has landed
// no data, so advancing the clock would hide exactly the outage being watched.
func TestSyncWindow_FailedSyncDoesNotRecordLastWindowSyncAt(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	connectedAt := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	attemptedAt := connectedAt.Add(30 * time.Hour)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", attemptedAt.Add(time.Hour), connectedAt)

	api := syncKindFixture()
	api.recErr = errors.New("whoop is down")
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return attemptedAt })
	if err := svc.SyncWindow(ctx, "u1", 25); err == nil {
		t.Fatal("SyncWindow: err = nil, want an upstream failure")
	}

	got, err := conns.Get(ctx, "u1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastWindowSyncAt == nil || !got.LastWindowSyncAt.Equal(connectedAt) {
		t.Fatalf("last_window_sync_at = %v, want it left at the connect seed %v", got.LastWindowSyncAt, connectedAt)
	}
}

// TestBackfillAndSyncSince_DoNotRecordLastWindowSyncAt is the durable-state
// twin of TestSyncSince_LabelsAdminResync: connect-time backfill and operator
// resync both land data, but neither proves the WEBHOOK path is alive. If they
// moved the timestamp, an operator resyncing to investigate an outage would
// silence the alert that told them about it.
func TestBackfillAndSyncSince_DoNotRecordLastWindowSyncAt(t *testing.T) {
	connectedAt := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	ranAt := connectedAt.Add(30 * time.Hour)

	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, svc *Service) error
	}{
		{"Backfill", func(ctx context.Context, svc *Service) error {
			return svc.Backfill(ctx, "u1")
		}},
		{"SyncSince", func(ctx context.Context, svc *Service) error {
			_, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			conns, rec := newRepos(t)
			cipher := newCipher(t)
			seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", ranAt.Add(time.Hour), connectedAt)

			svc := NewService(conns, rec, cipher, syncKindFixture(), &fakeRefresher{}, http.DefaultClient, func() time.Time { return ranAt })
			if err := tc.run(ctx, svc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			got, err := conns.Get(ctx, "u1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.LastWindowSyncAt == nil || !got.LastWindowSyncAt.Equal(connectedAt) {
				t.Fatalf("last_window_sync_at = %v, want it left at the connect seed %v", got.LastWindowSyncAt, connectedAt)
			}
		})
	}
}

// TestSyncSince_UpstreamErrorIsClassified pins that a WHOOP fetch failure is
// wrapped in ErrUpstream (so callers can map it to 502) and returns a zero-value
// result.
func TestSyncSince_UpstreamErrorIsClassified(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{recErr: errors.New("whoop 503")}
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if res != (SyncResult{}) {
		t.Fatalf("result = %+v, want zero value on upstream error", res)
	}
}

// TestSyncSince_NotConnectedReconnectNeeded confirms ErrReconnectNeeded flows
// through SyncSince unchanged (validToken classifies it before any fetch).
func TestSyncSince_NotConnectedReconnectNeeded(t *testing.T) {
	ctx := context.Background()
	conns, rec := newRepos(t)
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access", "refresh", now.Add(time.Hour), now)
	if err := conns.SetStatus(ctx, "u1", whoopconn.StatusError, now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	api := &fakeAPI{}
	svc := NewService(conns, rec, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
	if !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v, want ErrReconnectNeeded", err)
	}
	if res != (SyncResult{}) {
		t.Fatalf("result = %+v, want zero value on reconnect-needed", res)
	}
	if api.calls != 0 {
		t.Fatalf("api should not be called when not connected, got %d", api.calls)
	}
}
