package whoopsync

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

// --- webhook fakes ----------------------------------------------------------

// fakeWebhookConns implements whoopconn.Repository but only GetByWhoopUserID is
// exercised by the webhook; the rest panic to catch unexpected use. It maps a
// WHOOP user id to a canned connection (or ErrNotFound).
type fakeWebhookConns struct {
	whoopconn.Repository
	byWhoop map[int64]*whoopconn.Connection
}

func (f *fakeWebhookConns) GetByWhoopUserID(_ context.Context, whoopUserID int64) (*whoopconn.Connection, error) {
	conn, ok := f.byWhoop[whoopUserID]
	if !ok {
		return nil, whoopconn.ErrNotFound
	}
	return conn, nil
}

// fakeWebhookRec implements whooprecovery.Repository; only DeleteBySleepID is
// used by the webhook. It records the last delete call.
type fakeWebhookRec struct {
	whooprecovery.Repository
	deleteCalls int
	lastUserID  string
	lastSleepID string
	deleteErr   error
}

func (f *fakeWebhookRec) DeleteBySleepID(_ context.Context, userID, sleepID string) error {
	f.deleteCalls++
	f.lastUserID = userID
	f.lastSleepID = sleepID
	return f.deleteErr
}

// fakeSyncer records SyncWindow calls and optionally returns an error.
type fakeSyncer struct {
	calls      int
	lastUserID string
	lastLimit  int
	err        error
}

func (f *fakeSyncer) SyncWindow(_ context.Context, userID string, limit int) error {
	f.calls++
	f.lastUserID = userID
	f.lastLimit = limit
	return f.err
}

// --- helpers ----------------------------------------------------------------

// sign computes the base64 HMAC-SHA256 of (ts + body) exactly as WHOOP does.
func sign(secret []byte, ts string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// webhookHarness builds a WebhookHandler over the given fakes with a fixed
// clock, mounts it, and returns everything the tests assert on.
type webhookHarness struct {
	router chi.Router
	conns  *fakeWebhookConns
	rec    *fakeWebhookRec
	svc    *fakeSyncer
	secret []byte
	now    time.Time
}

func newWebhookHarness(t *testing.T) *webhookHarness {
	t.Helper()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	h := &webhookHarness{
		conns: &fakeWebhookConns{byWhoop: map[int64]*whoopconn.Connection{
			42: {UserID: "u1", WhoopUserID: 42, Status: whoopconn.StatusConnected},
		}},
		rec:    &fakeWebhookRec{},
		svc:    &fakeSyncer{},
		secret: []byte("whoop-client-secret"),
		now:    now,
	}
	wh := NewWebhookHandler(h.secret, h.conns, h.rec, h.svc, func() time.Time { return h.now })
	r := chi.NewRouter()
	wh.Mount(r)
	h.router = r
	return h
}

// post issues a signed (unless sig/ts overridden) POST /webhooks/whoop and
// returns the recorded response.
func (h *webhookHarness) do(t *testing.T, sig, ts string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/whoop", strings.NewReader(string(body)))
	if sig != "" {
		req.Header.Set("X-WHOOP-Signature", sig)
	}
	if ts != "" {
		req.Header.Set("X-WHOOP-Signature-Timestamp", ts)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *webhookHarness) tsNow() string {
	return strconv.FormatInt(h.now.UnixMilli(), 10)
}

// --- tests ------------------------------------------------------------------

func TestWebhook_RecoveryUpdated_Synced(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	ts := h.tsNow()
	rec := h.do(t, sign(h.secret, ts, body), ts, body)

	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("status = %d, want 2xx", rec.Code)
	}
	if h.svc.calls != 1 {
		t.Fatalf("SyncWindow calls = %d, want 1", h.svc.calls)
	}
	if h.svc.lastUserID != "u1" || h.svc.lastLimit != 10 {
		t.Fatalf("SyncWindow(%q, %d), want (u1, 10)", h.svc.lastUserID, h.svc.lastLimit)
	}
}

func TestWebhook_TamperedBody_401(t *testing.T) {
	h := newWebhookHarness(t)
	orig := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	ts := h.tsNow()
	sig := sign(h.secret, ts, orig)
	mutated := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-2"}`)

	rec := h.do(t, sig, ts, mutated)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if h.svc.calls != 0 {
		t.Fatalf("SyncWindow should not have been called")
	}
}

func TestWebhook_GarbageSignature_401(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	ts := h.tsNow()

	rec := h.do(t, "not-a-real-signature", ts, body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if h.svc.calls != 0 {
		t.Fatalf("SyncWindow should not have been called")
	}
}

func TestWebhook_StaleTimestamp_401(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	// 10 minutes in the past — outside the ±5m window. Sign over the stale ts so
	// only the freshness check (not the HMAC) is what rejects it.
	staleTS := strconv.FormatInt(h.now.Add(-10*time.Minute).UnixMilli(), 10)

	rec := h.do(t, sign(h.secret, staleTS, body), staleTS, body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if h.svc.calls != 0 {
		t.Fatalf("SyncWindow should not have been called")
	}
}

func TestWebhook_MissingHeaders_401(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)

	rec := h.do(t, "", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if h.svc.calls != 0 {
		t.Fatalf("SyncWindow should not have been called")
	}
}

func TestWebhook_UnknownUser_204(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":999,"type":"recovery.updated","id":"sleep-1"}`)
	ts := h.tsNow()

	rec := h.do(t, sign(h.secret, ts, body), ts, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if h.svc.calls != 0 {
		t.Fatalf("SyncWindow should not have been called for unknown user")
	}
}

func TestWebhook_NonConnectedUser_204(t *testing.T) {
	for _, status := range []whoopconn.Status{whoopconn.StatusRevoked, whoopconn.StatusError} {
		t.Run(string(status), func(t *testing.T) {
			h := newWebhookHarness(t)
			h.conns.byWhoop[42] = &whoopconn.Connection{UserID: "u1", WhoopUserID: 42, Status: status}
			body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
			ts := h.tsNow()

			rec := h.do(t, sign(h.secret, ts, body), ts, body)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			if h.svc.calls != 0 {
				t.Fatalf("SyncWindow should not have been called for %s user", status)
			}
		})
	}
}

func TestWebhook_RecoveryDeleted_Deletes(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":42,"type":"recovery.deleted","id":"sleep-xyz"}`)
	ts := h.tsNow()

	rec := h.do(t, sign(h.secret, ts, body), ts, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if h.rec.deleteCalls != 1 {
		t.Fatalf("DeleteBySleepID calls = %d, want 1", h.rec.deleteCalls)
	}
	if h.rec.lastUserID != "u1" || h.rec.lastSleepID != "sleep-xyz" {
		t.Fatalf("DeleteBySleepID(%q, %q), want (u1, sleep-xyz)", h.rec.lastUserID, h.rec.lastSleepID)
	}
	if h.svc.calls != 0 {
		t.Fatalf("SyncWindow should not have been called for a delete")
	}
}

func TestWebhook_OtherType_204NoSideEffects(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":42,"type":"workout.updated","id":"w-1"}`)
	ts := h.tsNow()

	rec := h.do(t, sign(h.secret, ts, body), ts, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if h.svc.calls != 0 || h.rec.deleteCalls != 0 {
		t.Fatalf("no side effects expected: sync=%d delete=%d", h.svc.calls, h.rec.deleteCalls)
	}
}

func TestWebhook_SyncError_500(t *testing.T) {
	h := newWebhookHarness(t)
	h.svc.err = context.DeadlineExceeded // stand-in for a transient sync failure
	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	ts := h.tsNow()

	rec := h.do(t, sign(h.secret, ts, body), ts, body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so WHOOP retries", rec.Code)
	}
}

func TestWebhook_BadJSONAfterValidSignature_400(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{not json`)
	ts := h.tsNow()

	rec := h.do(t, sign(h.secret, ts, body), ts, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Idempotency: the same valid delivery sent 5× (WHOOP's retry budget) forwards
// to SyncWindow each time; the service itself is idempotent (tested elsewhere).
func TestWebhook_RetriedDelivery_ForwardsEachTime(t *testing.T) {
	h := newWebhookHarness(t)
	body := []byte(`{"user_id":42,"type":"recovery.updated","id":"sleep-1"}`)
	ts := h.tsNow()
	sig := sign(h.secret, ts, body)

	for i := 0; i < 5; i++ {
		rec := h.do(t, sig, ts, body)
		if rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("delivery %d: status = %d, want 2xx", i, rec.Code)
		}
	}
	if h.svc.calls != 5 {
		t.Fatalf("SyncWindow calls = %d, want 5", h.svc.calls)
	}
}
