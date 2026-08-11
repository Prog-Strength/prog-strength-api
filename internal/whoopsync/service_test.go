package whoopsync

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopconn"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopsleep"
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
// callOrder so tests can assert the refresher ran before any API call, and
// keeps a call log so the scope-gate test can assert an under-scoped connection
// never reaches the wire at all — an outcome assertion alone would also pass
// for a call that was made and simply 403'd.
type fakeAPI struct {
	recoveries []Recovery
	cycles     []Cycle
	sleeps     []Sleep
	recErr     error // if set, Recoveries returns it (simulates a WHOOP fetch failure)
	cycErr     error // if set, Cycles returns it
	sleepErr   error // if set, Sleeps returns it
	lastToken  string
	calls      int32
	orderTick  *int32 // shared monotonic counter
	firstOrder int32  // order tick of the first API call

	logMu   sync.Mutex
	callLog []string
}

func (f *fakeAPI) record(method string) {
	f.logMu.Lock()
	defer f.logMu.Unlock()
	f.callLog = append(f.callLog, method)
}

func (f *fakeAPI) called(method string) bool {
	f.logMu.Lock()
	defer f.logMu.Unlock()
	for _, m := range f.callLog {
		if m == method {
			return true
		}
	}
	return false
}

func (f *fakeAPI) Recoveries(_ context.Context, accessToken string, _, _ time.Time, _ int) ([]Recovery, error) {
	f.record("Recoveries")
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
	f.record("Cycles")
	f.lastToken = accessToken
	if f.cycErr != nil {
		return nil, f.cycErr
	}
	return f.cycles, nil
}

func (f *fakeAPI) Sleeps(_ context.Context, accessToken string, _, _ time.Time, _ int) ([]Sleep, error) {
	f.record("Sleeps")
	f.lastToken = accessToken
	if f.sleepErr != nil {
		return nil, f.sleepErr
	}
	return f.sleeps, nil
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
// access/refresh plaintext and token expiry, granting the full ScopeString.
func seedConnection(t *testing.T, conns whoopconn.Repository, cipher *tokencrypt.Cipher, userID, access, refresh string, expiresAt, now time.Time) {
	t.Helper()
	seedConnectionScopes(t, conns, cipher, userID, access, refresh, expiresAt, now, ScopeString)
}

// seedConnectionScopes is seedConnection with an explicit granted-scope string,
// for the under-scoped (connected but degraded) case.
func seedConnectionScopes(t *testing.T, conns whoopconn.Repository, cipher *tokencrypt.Cipher, userID, access, refresh string, expiresAt, now time.Time, scopes string) {
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
	if err := conns.Upsert(ctx, userID, 42, bundle, scopes, now); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

// newRepos builds the three repositories a Service needs over one ephemeral DB,
// seeding a users row for each id the test will sync. The seed is not optional:
// user_whoop_sleep carries a real foreign key to users (user_whoop_recovery,
// predating it, does not), so a sleep upsert for an unseeded id fails.
func newRepos(t *testing.T, userIDs ...string) (whoopconn.Repository, whooprecovery.Repository, whoopsleep.Repository) {
	t.Helper()
	database := dbtest.New(t)
	for _, uid := range userIDs {
		seedUser(t, database, uid)
	}
	return whoopconn.NewSQLiteRepository(database), whooprecovery.NewSQLiteRepository(database), whoopsleep.NewSQLiteRepository(database)
}

func seedUser(t *testing.T, database *sql.DB, userID string) {
	t.Helper()
	seededAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := database.Exec(`
		INSERT INTO users (id, email, display_name, weight_unit, created_at, updated_at)
		VALUES (?, ?, ?, 'lb', ?, ?)`, userID, userID+"@example.com", userID, seededAt, seededAt)
	if err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
}

// --- SyncWindow: SCORED-only, missing-cycle skip ----------------------------

func TestSyncWindow_UpsertsOnlyScoredSkipsMissingCycle(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
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

	svc := NewService(conns, rec, slp, cipher, api, refr, http.DefaultClient, func() time.Time { return now })

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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{
		cycles:     []Cycle{{ID: 1, Start: "2026-01-15T12:00:00Z", TimezoneOffset: "-08:00"}},
		recoveries: []Recovery{{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-15T15:30:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(72)}}},
	}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

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
	conns, rec, slp := newRepos(t, "u1")
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

	svc := NewService(conns, rec, slp, cipher, api, refr, http.DefaultClient, func() time.Time { return now })

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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "old-access", "old-refresh", now.Add(-time.Minute), now)

	refr := &fakeRefresher{err: ErrInvalidGrant}
	api := &fakeAPI{}
	svc := NewService(conns, rec, slp, cipher, api, refr, http.DefaultClient, func() time.Time { return now })

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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access", "refresh", now.Add(time.Hour), now)

	// Flip to error (not connected).
	if err := conns.SetStatus(ctx, "u1", whoopconn.StatusError, now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	api := &fakeAPI{}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v, want ErrReconnectNeeded", err)
	}
	if api.calls != 0 {
		t.Fatalf("api should not be called when not connected, got %d", api.calls)
	}
}

func TestSyncWindow_NoConnectionReconnectNeeded(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	svc := NewService(conns, rec, slp, cipher, &fakeAPI{}, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "ghost", 10); !errors.Is(err, ErrReconnectNeeded) {
		t.Fatalf("err = %v, want ErrReconnectNeeded", err)
	}
}

// --- Backfill wiring --------------------------------------------------------

func TestBackfill_SyncsWiderWindow(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access", "refresh", now.Add(time.Hour), now)

	api := &fakeAPI{
		cycles:     []Cycle{{ID: 1, Start: "2026-01-02T12:00:00Z", TimezoneOffset: "+00:00"}},
		recoveries: []Recovery{{CycleID: 1, SleepID: "s1", CreatedAt: "2026-01-02T07:10:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RecoveryScore: fptr(65)}}},
	}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 16, 20, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{
		// Bedtime 22:45 local Jan 15 (-08:00) = 06:45Z Jan 16.
		cycles: []Cycle{{ID: 7, Start: "2026-01-16T06:45:00Z", TimezoneOffset: "-08:00"}},
		// Scored 07:05 local Jan 16 = 15:05Z.
		recoveries: []Recovery{{CycleID: 7, SleepID: "s7", CreatedAt: "2026-01-16T15:05:00Z", ScoreState: "SCORED", Score: &RecoveryScore{RestingHeartRate: fptr(52)}}},
	}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

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
	conns, rec, slp := newRepos(t, "u1")
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
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	adminBefore := testutil.ToFloat64(syncsTotal.WithLabelValues(domainRecovery, "admin_resync", "ok"))
	windowBefore := testutil.ToFloat64(syncsTotal.WithLabelValues(domainRecovery, "window", "ok"))

	res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
	if err != nil {
		t.Fatalf("SyncSince: %v", err)
	}
	if res.Upserted != 2 {
		t.Fatalf("Upserted = %d, want 2 (result: %+v)", res.Upserted, res)
	}

	if got := testutil.ToFloat64(syncsTotal.WithLabelValues(domainRecovery, "admin_resync", "ok")) - adminBefore; got != 1 {
		t.Fatalf("{admin_resync,ok} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(syncsTotal.WithLabelValues(domainRecovery, "window", "ok")) - windowBefore; got != 0 {
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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	connectedAt := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	syncedAt := connectedAt.Add(30 * time.Hour)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", syncedAt.Add(time.Hour), connectedAt)

	svc := NewService(conns, rec, slp, cipher, syncKindFixture(), &fakeRefresher{}, http.DefaultClient, func() time.Time { return syncedAt })
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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	connectedAt := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	attemptedAt := connectedAt.Add(30 * time.Hour)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", attemptedAt.Add(time.Hour), connectedAt)

	api := syncKindFixture()
	api.recErr = errors.New("whoop is down")
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return attemptedAt })
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
			conns, rec, slp := newRepos(t, "u1")
			cipher := newCipher(t)
			seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", ranAt.Add(time.Hour), connectedAt)

			svc := NewService(conns, rec, slp, cipher, syncKindFixture(), &fakeRefresher{}, http.DefaultClient, func() time.Time { return ranAt })
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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{recErr: errors.New("whoop 503")}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

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
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access", "refresh", now.Add(time.Hour), now)
	if err := conns.SetStatus(ctx, "u1", whoopconn.StatusError, now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	api := &fakeAPI{}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

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

// --- sleep sync path --------------------------------------------------------

func i64ptr(i int64) *int64 { return &i }

// scoredSleep is a SCORED night with every documented field populated, so a
// test can vary one field without restating twenty.
func scoredSleep(id, start, end, off string, nap bool) Sleep {
	return Sleep{
		ID:             id,
		Start:          start,
		End:            end,
		TimezoneOffset: off,
		Nap:            nap,
		ScoreState:     "SCORED",
		Score: &SleepScore{
			StageSummary: &SleepStageSummary{
				TotalInBedTimeMilli:         i64ptr(27300000),
				TotalAwakeTimeMilli:         i64ptr(1800000),
				TotalNoDataTimeMilli:        i64ptr(0),
				TotalLightSleepTimeMilli:    i64ptr(12000000),
				TotalSlowWaveSleepTimeMilli: i64ptr(7500000),
				TotalRemSleepTimeMilli:      i64ptr(6000000),
				SleepCycleCount:             i64ptr(5),
				DisturbanceCount:            i64ptr(9),
			},
			SleepNeeded: &SleepNeeded{
				BaselineMilli:             i64ptr(28000000),
				NeedFromSleepDebtMilli:    i64ptr(1200000),
				NeedFromRecentStrainMilli: i64ptr(900000),
				NeedFromRecentNapMilli:    i64ptr(-600000),
			},
			RespiratoryRate:            fptr(14.7),
			SleepPerformancePercentage: fptr(92.5),
			SleepConsistencyPercentage: fptr(71),
			SleepEfficiencyPercentage:  fptr(88.25),
		},
	}
}

// TestSyncWindow_UpsertsScoredSleepUnconverted pins the happy path: one row per
// WHOOP record, dated by END rather than start, and every millisecond value
// carried through exactly as WHOOP sent it (the ingest layer converts nothing).
func TestSyncWindow_UpsertsScoredSleepUnconverted(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 3, 3, 20, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{sleeps: []Sleep{scoredSleep("sleep-1", "2026-03-02T22:40:00-06:00", "2026-03-03T06:15:00-06:00", "-06:00", false)}}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}
	got, err := slp.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 sleep row, got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.WhoopSleepID != "sleep-1" || e.Date != "2026-03-03" || e.IsNap || e.ScoreState != "SCORED" {
		t.Fatalf("unexpected row: %+v", e)
	}
	if e.StartedAt != "2026-03-02T22:40:00-06:00" || e.EndedAt != "2026-03-03T06:15:00-06:00" || e.TimezoneOffset != "-06:00" {
		t.Fatalf("instants not stored as WHOOP sent them: %+v", e)
	}
	for _, tc := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"InBedMilli", e.InBedMilli, 27300000},
		{"AwakeMilli", e.AwakeMilli, 1800000},
		{"NoDataMilli", e.NoDataMilli, 0},
		{"LightSleepMilli", e.LightSleepMilli, 12000000},
		{"SlowWaveSleepMilli", e.SlowWaveSleepMilli, 7500000},
		{"REMSleepMilli", e.REMSleepMilli, 6000000},
		{"SleepCycleCount", e.SleepCycleCount, 5},
		{"DisturbanceCount", e.DisturbanceCount, 9},
		{"NeedBaselineMilli", e.NeedBaselineMilli, 28000000},
		{"NeedFromSleepDebtMilli", e.NeedFromSleepDebtMilli, 1200000},
		{"NeedFromStrainMilli", e.NeedFromStrainMilli, 900000},
		{"NeedFromNapMilli", e.NeedFromNapMilli, -600000},
	} {
		if tc.got == nil {
			t.Errorf("%s = nil, want %d", tc.name, tc.want)
			continue
		}
		if *tc.got != tc.want {
			t.Errorf("%s = %d, want %d (converted at ingest?)", tc.name, *tc.got, tc.want)
		}
	}
	if e.RespiratoryRate == nil || *e.RespiratoryRate != 14.7 ||
		e.PerformancePct == nil || *e.PerformancePct != 92.5 ||
		e.ConsistencyPct == nil || *e.ConsistencyPct != 71 ||
		e.EfficiencyPct == nil || *e.EfficiencyPct != 88.25 {
		t.Errorf("scored percentages not copied: %+v", e)
	}
}

// TestSyncSleepWindow_DatesNightByWakeNotBedtime is the named regression for the
// bug migration 041_wipe_whoop_recovery_redate.sql exists to commemorate. A
// night starting 22:40 Monday and ending 06:15 Tuesday is TUESDAY's sleep: it is
// the night the user is recovering from when they open the dashboard on Tuesday.
// Dating by `start` would put it on Monday AND on a different day than the
// recovery computed from the same night, making two tiles disagree.
func TestSyncSleepWindow_DatesNightByWakeNotBedtime(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 3, 3, 20, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{sleeps: []Sleep{scoredSleep("night", "2026-03-02T22:40:00-06:00", "2026-03-03T06:15:00-06:00", "-06:00", false)}}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}
	got, err := slp.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if got[0].Date != "2026-03-03" {
		t.Fatalf("date = %q (bedtime day?), want 2026-03-03, the day the user woke up", got[0].Date)
	}
}

// TestSyncSleepWindow_UsesEachRecordsOwnOffsetAcrossDST pins that DST is handled
// by construction: the offset used is the one in force at that record's end, as
// WHOOP recorded it per record, so two nights either side of a spring-forward
// land on the dates their OWN offsets imply. No IANA lookup, no shared offset.
func TestSyncSleepWindow_UsesEachRecordsOwnOffsetAcrossDST(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{sleeps: []Sleep{
		// Before US spring-forward: -07:00. Ends 23:30 local Mar 7.
		scoredSleep("before-dst", "2026-03-07T15:00:00Z", "2026-03-08T06:30:00Z", "-07:00", false),
		// After it: -06:00. The same UTC instant is now Mar 8 local.
		scoredSleep("after-dst", "2026-03-08T14:00:00Z", "2026-03-09T05:30:00Z", "-06:00", false),
	}}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}
	got, err := slp.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	dates := map[string]string{}
	for _, e := range got {
		dates[e.WhoopSleepID] = e.Date
	}
	if dates["before-dst"] != "2026-03-07" {
		t.Errorf("before-dst date = %q, want 2026-03-07 (from its own -07:00)", dates["before-dst"])
	}
	if dates["after-dst"] != "2026-03-08" {
		t.Errorf("after-dst date = %q, want 2026-03-08 (from its own -06:00)", dates["after-dst"])
	}
}

// TestSyncSleepWindow_NapLandsOnItsOwnDay pins that naps are ingested (dropping
// them would corrupt WHOOP's sleep-need math) and dated by their own end, which
// for a 14:00 nap is simply the day it happened.
func TestSyncSleepWindow_NapLandsOnItsOwnDay(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 3, 3, 23, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{sleeps: []Sleep{
		scoredSleep("night", "2026-03-02T22:40:00-06:00", "2026-03-03T06:15:00-06:00", "-06:00", false),
		scoredSleep("nap", "2026-03-03T14:00:00-06:00", "2026-03-03T14:45:00-06:00", "-06:00", true),
	}}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); err != nil {
		t.Fatalf("SyncWindow: %v", err)
	}
	got, err := slp.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows (a nap is a sleep record), got %d: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Date != "2026-03-03" {
			t.Errorf("%s date = %q, want 2026-03-03", e.WhoopSleepID, e.Date)
		}
		if (e.WhoopSleepID == "nap") != e.IsNap {
			t.Errorf("%s IsNap = %v", e.WhoopSleepID, e.IsNap)
		}
	}
}

// TestSyncSleepWindow_StoresUnscoredRecordsAndCountsThem pins the semantics the
// two readings of skipped_unscored would otherwise coin-flip on: a PENDING or
// UNSCORABLE record IS stored (so the row exists for the score to land in) and
// is ALSO counted as skipped_unscored, because the counter means "no score to
// store", not "no row written".
func TestSyncSleepWindow_StoresUnscoredRecordsAndCountsThem(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 3, 3, 20, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{sleeps: []Sleep{
		{ID: "pending", Start: "2026-03-02T22:40:00-06:00", End: "2026-03-03T06:15:00-06:00", TimezoneOffset: "-06:00", ScoreState: "PENDING"},
		{ID: "unscorable", Start: "2026-03-03T22:40:00-06:00", End: "2026-03-04T06:15:00-06:00", TimezoneOffset: "-06:00", ScoreState: "UNSCORABLE"},
	}}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
	if err != nil {
		t.Fatalf("SyncSince: %v", err)
	}
	if res.Sleep.Upserted != 2 {
		t.Errorf("Sleep.Upserted = %d, want 2 (an unscored record is still stored)", res.Sleep.Upserted)
	}
	if res.Sleep.SkippedUnscored != 2 {
		t.Errorf("Sleep.SkippedUnscored = %d, want 2", res.Sleep.SkippedUnscored)
	}
	got, err := slp.ListRange(ctx, "u1", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	for _, e := range got {
		if e.InBedMilli != nil || e.REMSleepMilli != nil || e.PerformancePct != nil {
			t.Errorf("%s carries score fields: %+v", e.WhoopSleepID, e)
		}
	}
}

// TestSyncSleepWindow_BadDateSkippedWithoutFailingSync pins that an undateable
// record is dropped with a warn and a counter rather than guessing a date or
// taking the whole sync down with it.
func TestSyncSleepWindow_BadDateSkippedWithoutFailingSync(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 3, 3, 20, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := &fakeAPI{sleeps: []Sleep{
		scoredSleep("ok", "2026-03-02T22:40:00-06:00", "2026-03-03T06:15:00-06:00", "-06:00", false),
		scoredSleep("bad-offset", "2026-03-03T22:40:00Z", "2026-03-04T06:15:00Z", "-6:00", false),
		scoredSleep("bad-end", "2026-03-04T22:40:00Z", "not-a-time", "-06:00", false),
	}}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
	if err != nil {
		t.Fatalf("SyncSince: %v", err)
	}
	if res.Sleep.Err != nil {
		t.Errorf("Sleep.Err = %v, want nil (a bad date is data drift, not a failure)", res.Sleep.Err)
	}
	if res.Sleep.SkippedBadDate != 2 {
		t.Errorf("Sleep.SkippedBadDate = %d, want 2", res.Sleep.SkippedBadDate)
	}
	if res.Sleep.Upserted != 1 {
		t.Errorf("Sleep.Upserted = %d, want 1", res.Sleep.Upserted)
	}
}

// TestSyncSleepWindow_FailureDoesNotFailRecoverySync is the isolation
// requirement, not an optimization: the webhook must not 500 on a sleep-only
// failure, because a 500 makes WHOOP redeliver the recovery data we already
// stored successfully.
func TestSyncSleepWindow_FailureDoesNotFailRecoverySync(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sleepErr error
	}{
		{"forbidden", errors.New("whoopsync: whoop returned status 403")},
		{"rate limited", &RateLimitError{RetryAfter: "30"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			conns, rec, slp := newRepos(t, "u1")
			cipher := newCipher(t)
			now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
			seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

			api := syncKindFixture()
			api.sleepErr = tc.sleepErr
			svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

			sleepErrs := syncsTotal.WithLabelValues(domainSleep, kindAdminResync, "error")
			recoveryOKs := syncsTotal.WithLabelValues(domainRecovery, kindAdminResync, "ok")
			sleepBefore := testutil.ToFloat64(sleepErrs)
			recoveryBefore := testutil.ToFloat64(recoveryOKs)

			res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
			if err != nil {
				t.Fatalf("SyncSince err = %v, want nil: a sleep failure must not fail the overall sync", err)
			}
			if res.Sleep.Err == nil {
				t.Fatal("Sleep.Err = nil, want the sleep-side failure reported in the result")
			}
			if res.Upserted != 2 {
				t.Errorf("Upserted = %d, want 2: recovery rows must land regardless", res.Upserted)
			}
			gotRows, err := rec.ListRange(ctx, "u1", "", "")
			if err != nil {
				t.Fatalf("list recoveries: %v", err)
			}
			if len(gotRows) != 2 {
				t.Errorf("recovery rows = %d, want 2", len(gotRows))
			}
			if got := testutil.ToFloat64(sleepErrs) - sleepBefore; got != 1 {
				t.Errorf("syncs{sleep,error} delta = %v, want 1", got)
			}
			if got := testutil.ToFloat64(recoveryOKs) - recoveryBefore; got != 1 {
				t.Errorf("syncs{recovery,ok} delta = %v, want 1", got)
			}
		})
	}
}

// TestSyncWindow_RecoveryFailureSkipsSleepEntirely pins the other direction: a
// recovery failure keeps its existing behavior exactly — the error is returned
// and the sleep path is not even attempted.
func TestSyncWindow_RecoveryFailureSkipsSleepEntirely(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now)

	api := syncKindFixture()
	api.recErr = errors.New("whoop is down")
	api.sleeps = []Sleep{scoredSleep("night", "2026-01-19T22:40:00-06:00", "2026-01-20T06:15:00-06:00", "-06:00", false)}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	if err := svc.SyncWindow(ctx, "u1", 10); !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
	if api.called("Sleeps") {
		t.Errorf("call log = %v, want no Sleeps call after a recovery failure", api.callLog)
	}
	count, err := slp.CountForUser(ctx, "u1")
	if err != nil {
		t.Fatalf("CountForUser: %v", err)
	}
	if count != 0 {
		t.Errorf("sleep rows = %d, want 0", count)
	}
}

// TestSyncSleepWindow_UnderScopedConnectionMakesNoHTTPCall pins the scope gate.
// The assertion is against the call log rather than the outcome: an outcome-only
// check would also pass for a request that WAS made and simply 403'd, which is
// exactly the rate-limit-burning behavior the gate exists to prevent.
func TestSyncSleepWindow_UnderScopedConnectionMakesNoHTTPCall(t *testing.T) {
	ctx := context.Background()
	conns, rec, slp := newRepos(t, "u1")
	cipher := newCipher(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	// A connection made before read:sleep was requested: connected, valid
	// tokens, recovery still syncing, but no sleep grant.
	seedConnectionScopes(t, conns, cipher, "u1", "access-tok", "refresh-tok", now.Add(time.Hour), now,
		"read:recovery read:cycles read:profile offline")

	api := syncKindFixture()
	api.sleeps = []Sleep{scoredSleep("night", "2026-01-19T22:40:00-06:00", "2026-01-20T06:15:00-06:00", "-06:00", false)}
	svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return now })

	noScope := sleepRowsTotal.WithLabelValues("skipped_no_scope")
	before := testutil.ToFloat64(noScope)

	res, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
	if err != nil {
		t.Fatalf("SyncSince: %v", err)
	}
	if api.called("Sleeps") {
		t.Errorf("call log = %v, want NO Sleeps call for an under-scoped connection", api.callLog)
	}
	if !res.Sleep.SkippedNoScope {
		t.Error("Sleep.SkippedNoScope = false, want true")
	}
	if res.Sleep.Err != nil {
		t.Errorf("Sleep.Err = %v, want nil: an unreconnected user is not an error", res.Sleep.Err)
	}
	if res.Upserted != 2 {
		t.Errorf("Upserted = %d, want 2: recovery keeps syncing for an under-scoped connection", res.Upserted)
	}
	if got := testutil.ToFloat64(noScope) - before; got != 1 {
		t.Errorf("sleep_rows{skipped_no_scope} delta = %v, want 1", got)
	}
}

// TestMarkWindowSync_UnaffectedBySleepOutcome pins that the single
// per-connection liveness stamp still tracks the WEBHOOK path and nothing else:
// only kind="window" advances it, and a sleep-only failure — which by design
// does not fail the sync — must not withhold it, or a sleep outage would page
// as a dead recovery ingestion.
func TestMarkWindowSync_UnaffectedBySleepOutcome(t *testing.T) {
	connectedAt := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	ranAt := connectedAt.Add(30 * time.Hour)

	for _, tc := range []struct {
		name      string
		run       func(ctx context.Context, svc *Service) error
		wantStamp time.Time
	}{
		{"window advances despite a sleep failure", func(ctx context.Context, svc *Service) error {
			return svc.SyncWindow(ctx, "u1", 25)
		}, ranAt},
		{"backfill still does not advance", func(ctx context.Context, svc *Service) error {
			return svc.Backfill(ctx, "u1")
		}, connectedAt},
		{"admin resync still does not advance", func(ctx context.Context, svc *Service) error {
			_, err := svc.SyncSince(ctx, "u1", 30*24*time.Hour, 25)
			return err
		}, connectedAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			conns, rec, slp := newRepos(t, "u1")
			cipher := newCipher(t)
			seedConnection(t, conns, cipher, "u1", "access-tok", "refresh-tok", ranAt.Add(time.Hour), connectedAt)

			api := syncKindFixture()
			api.sleepErr = errors.New("whoopsync: whoop returned status 403")
			svc := NewService(conns, rec, slp, cipher, api, &fakeRefresher{}, http.DefaultClient, func() time.Time { return ranAt })
			if err := tc.run(ctx, svc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			got, err := conns.Get(ctx, "u1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.LastWindowSyncAt == nil || !got.LastWindowSyncAt.Equal(tc.wantStamp) {
				t.Fatalf("last_window_sync_at = %v, want %v", got.LastWindowSyncAt, tc.wantStamp)
			}
		})
	}
}
