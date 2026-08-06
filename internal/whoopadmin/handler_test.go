package whoopadmin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Prog-Strength/prog-strength-api/internal/whoopadmin"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopconn"
	"github.com/Prog-Strength/prog-strength-api/internal/whooprecovery"
	"github.com/Prog-Strength/prog-strength-api/internal/whoopsync"
)

// --- fakes implementing the three narrow consumer interfaces ---

type fakeConns struct {
	list []whoopconn.Connection
	byID map[string]*whoopconn.Connection
}

func (f *fakeConns) List(context.Context) ([]whoopconn.Connection, error) {
	return f.list, nil
}

func (f *fakeConns) Get(_ context.Context, userID string) (*whoopconn.Connection, error) {
	c, ok := f.byID[userID]
	if !ok {
		return nil, whoopconn.ErrNotFound
	}
	return c, nil
}

type fakeRecovery struct {
	// latest is keyed by user; a nil value means (nil, nil) — no rows.
	latest map[string]*whooprecovery.Entry
	counts map[string]int
}

func (f *fakeRecovery) Latest(_ context.Context, userID string) (*whooprecovery.Entry, error) {
	return f.latest[userID], nil
}

func (f *fakeRecovery) CountForUser(_ context.Context, userID string) (int, error) {
	return f.counts[userID], nil
}

// fakeResyncer records the window it was called with and returns a canned
// result/error so tests can assert both the mapping and the clamp logic.
type fakeResyncer struct {
	res       whoopsync.SyncResult
	err       error
	gotWindow time.Duration
	gotLimit  int
	callCount int
	gotUserID string
}

func (f *fakeResyncer) SyncSince(_ context.Context, userID string, window time.Duration, limit int) (whoopsync.SyncResult, error) {
	f.callCount++
	f.gotUserID = userID
	f.gotWindow = window
	f.gotLimit = limit
	return f.res, f.err
}

// envelope mirrors the httpresp success shape enough to read data back out.
type envelope struct {
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

func newRouter(h *whoopadmin.Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func decodeEnvelope(t *testing.T, body []byte) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, body)
	}
	return e
}

func fixedNow(ts string) func() time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

func TestListConnections(t *testing.T) {
	// now is 2026-07-31T12:00:00Z. userA's token expired yesterday; userB's
	// expires tomorrow, so token_expired must be true then false.
	now := fixedNow("2026-07-31T12:00:00Z")
	expired := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	valid := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	connectedAt := time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)

	conns := &fakeConns{
		list: []whoopconn.Connection{
			{
				UserID:         "userA",
				WhoopUserID:    111,
				Scopes:         "read:recovery",
				Status:         whoopconn.StatusConnected,
				TokenExpiresAt: expired,
				ConnectedAt:    connectedAt,
				UpdatedAt:      updatedAt,
			},
			{
				UserID:         "userB",
				WhoopUserID:    222,
				Scopes:         "read:recovery offline",
				Status:         whoopconn.StatusConnected,
				TokenExpiresAt: valid,
				ConnectedAt:    connectedAt,
				UpdatedAt:      updatedAt,
			},
		},
	}
	rec := &fakeRecovery{
		latest: map[string]*whooprecovery.Entry{
			"userA": {Date: "2026-07-30"},
			"userB": nil, // no rows
		},
		counts: map[string]int{"userA": 5, "userB": 0},
	}

	h := whoopadmin.NewHandler(conns, rec, &fakeResyncer{}, now)
	srv := newRouter(h)

	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/whoop/connections", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}

	env := decodeEnvelope(t, resp.Body.Bytes())
	var data struct {
		Connections []struct {
			UserID             string  `json:"user_id"`
			WhoopUserID        int64   `json:"whoop_user_id"`
			Status             string  `json:"status"`
			Scopes             string  `json:"scopes"`
			TokenExpiresAt     string  `json:"token_expires_at"`
			TokenExpired       bool    `json:"token_expired"`
			ConnectedAt        string  `json:"connected_at"`
			UpdatedAt          string  `json:"updated_at"`
			LatestRecoveryDate *string `json:"latest_recovery_date"`
			RecoveryRowCount   int     `json:"recovery_row_count"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(data.Connections) != 2 {
		t.Fatalf("connections len = %d, want 2", len(data.Connections))
	}

	a := data.Connections[0]
	if !a.TokenExpired {
		t.Errorf("userA token_expired = false, want true")
	}
	if a.LatestRecoveryDate == nil || *a.LatestRecoveryDate != "2026-07-30" {
		t.Errorf("userA latest_recovery_date = %v, want 2026-07-30", a.LatestRecoveryDate)
	}
	if a.RecoveryRowCount != 5 {
		t.Errorf("userA recovery_row_count = %d, want 5", a.RecoveryRowCount)
	}
	if a.TokenExpiresAt != expired.UTC().Format(time.RFC3339) {
		t.Errorf("userA token_expires_at = %q, want %q", a.TokenExpiresAt, expired.UTC().Format(time.RFC3339))
	}
	if a.ConnectedAt != connectedAt.UTC().Format(time.RFC3339) {
		t.Errorf("userA connected_at = %q, want RFC3339", a.ConnectedAt)
	}
	if a.UpdatedAt != updatedAt.UTC().Format(time.RFC3339) {
		t.Errorf("userA updated_at = %q, want RFC3339", a.UpdatedAt)
	}

	b := data.Connections[1]
	if b.TokenExpired {
		t.Errorf("userB token_expired = true, want false")
	}
	if b.LatestRecoveryDate != nil {
		t.Errorf("userB latest_recovery_date = %v, want null", *b.LatestRecoveryDate)
	}
	if b.RecoveryRowCount != 0 {
		t.Errorf("userB recovery_row_count = %d, want 0", b.RecoveryRowCount)
	}
}

func TestListConnections_EmptyIsArrayNotNull(t *testing.T) {
	h := whoopadmin.NewHandler(&fakeConns{}, &fakeRecovery{}, &fakeResyncer{}, fixedNow("2026-07-31T12:00:00Z"))
	srv := newRouter(h)

	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/whoop/connections", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	// The raw body must contain "connections":[] not "connections":null.
	if !bytes.Contains(resp.Body.Bytes(), []byte(`"connections":[]`)) {
		t.Errorf("body = %s, want connections to be []", resp.Body.String())
	}
}

func TestGetConnection_OK(t *testing.T) {
	now := fixedNow("2026-07-31T12:00:00Z")
	conn := &whoopconn.Connection{
		UserID:         "userA",
		WhoopUserID:    111,
		Scopes:         "read:recovery",
		Status:         whoopconn.StatusConnected,
		TokenExpiresAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		ConnectedAt:    time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
	}
	conns := &fakeConns{byID: map[string]*whoopconn.Connection{"userA": conn}}
	rec := &fakeRecovery{
		latest: map[string]*whooprecovery.Entry{"userA": {Date: "2026-07-29"}},
		counts: map[string]int{"userA": 3},
	}
	h := whoopadmin.NewHandler(conns, rec, &fakeResyncer{}, now)
	srv := newRouter(h)

	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/whoop/connections/userA", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	env := decodeEnvelope(t, resp.Body.Bytes())
	var view struct {
		UserID             string  `json:"user_id"`
		LatestRecoveryDate *string `json:"latest_recovery_date"`
		RecoveryRowCount   int     `json:"recovery_row_count"`
	}
	if err := json.Unmarshal(env.Data, &view); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if view.UserID != "userA" {
		t.Errorf("user_id = %q, want userA", view.UserID)
	}
	if view.LatestRecoveryDate == nil || *view.LatestRecoveryDate != "2026-07-29" {
		t.Errorf("latest_recovery_date = %v, want 2026-07-29", view.LatestRecoveryDate)
	}
	if view.RecoveryRowCount != 3 {
		t.Errorf("recovery_row_count = %d, want 3", view.RecoveryRowCount)
	}
}

func TestGetConnection_NotFound(t *testing.T) {
	h := whoopadmin.NewHandler(&fakeConns{byID: map[string]*whoopconn.Connection{}}, &fakeRecovery{}, &fakeResyncer{}, fixedNow("2026-07-31T12:00:00Z"))
	srv := newRouter(h)

	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/whoop/connections/ghost", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", resp.Code, resp.Body.String())
	}
}

// resyncFixture builds a handler whose Get returns a connected connection for
// "userA", plus the shared resyncer spy for window assertions.
func resyncFixture(res whoopsync.SyncResult, syncErr error) (*whoopadmin.Handler, *fakeResyncer) {
	conn := &whoopconn.Connection{UserID: "userA", Status: whoopconn.StatusConnected}
	conns := &fakeConns{byID: map[string]*whoopconn.Connection{"userA": conn}}
	svc := &fakeResyncer{res: res, err: syncErr}
	h := whoopadmin.NewHandler(conns, &fakeRecovery{}, svc, fixedNow("2026-07-31T12:00:00Z"))
	return h, svc
}

func postResync(t *testing.T, srv http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/admin/whoop/resync", bytes.NewReader(b)))
	return resp
}

func TestResync_HappyPath(t *testing.T) {
	want := whoopsync.SyncResult{Upserted: 4, SkippedUnscored: 1, SkippedNoCycle: 2, SkippedBadDate: 3}
	h, svc := resyncFixture(want, nil)
	srv := newRouter(h)

	resp := postResync(t, srv, map[string]any{"user_id": "userA", "window_days": 30})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	env := decodeEnvelope(t, resp.Body.Bytes())
	var out struct {
		Upserted        int `json:"upserted"`
		SkippedUnscored int `json:"skipped_unscored"`
		SkippedNoCycle  int `json:"skipped_no_cycle"`
		SkippedBadDate  int `json:"skipped_bad_date"`
	}
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if out.Upserted != 4 || out.SkippedUnscored != 1 || out.SkippedNoCycle != 2 || out.SkippedBadDate != 3 {
		t.Errorf("outcome = %+v, want mapped counts", out)
	}
	if svc.gotUserID != "userA" {
		t.Errorf("SyncSince userID = %q, want userA", svc.gotUserID)
	}
	if svc.gotWindow != 30*24*time.Hour {
		t.Errorf("window = %v, want 30 days", svc.gotWindow)
	}
	if svc.gotLimit != 25 {
		t.Errorf("limit = %d, want 25", svc.gotLimit)
	}
}

func TestResync_WindowDaysClampAndDefault(t *testing.T) {
	cases := []struct {
		name       string
		body       map[string]any
		wantWindow time.Duration
	}{
		{"omitted defaults to 30", map[string]any{"user_id": "userA"}, 30 * 24 * time.Hour},
		{"zero defaults to 30", map[string]any{"user_id": "userA", "window_days": 0}, 30 * 24 * time.Hour},
		{"negative defaults to 30", map[string]any{"user_id": "userA", "window_days": -5}, 30 * 24 * time.Hour},
		{"one stays one", map[string]any{"user_id": "userA", "window_days": 1}, 1 * 24 * time.Hour},
		{"over max clamps to 90", map[string]any{"user_id": "userA", "window_days": 1000}, 90 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, svc := resyncFixture(whoopsync.SyncResult{}, nil)
			srv := newRouter(h)
			resp := postResync(t, srv, tc.body)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", resp.Code, resp.Body.String())
			}
			if svc.gotWindow != tc.wantWindow {
				t.Errorf("window = %v, want %v", svc.gotWindow, tc.wantWindow)
			}
		})
	}
}

func TestResync_InvalidBody(t *testing.T) {
	h, _ := resyncFixture(whoopsync.SyncResult{}, nil)
	srv := newRouter(h)
	resp := httptest.NewRecorder()
	srv.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/admin/whoop/resync", bytes.NewReader([]byte("{not json"))))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", resp.Code, resp.Body.String())
	}
}

func TestResync_EmptyUserID(t *testing.T) {
	h, svc := resyncFixture(whoopsync.SyncResult{}, nil)
	srv := newRouter(h)
	resp := postResync(t, srv, map[string]any{"user_id": "", "window_days": 30})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", resp.Code, resp.Body.String())
	}
	if svc.callCount != 0 {
		t.Errorf("SyncSince called %d times, want 0", svc.callCount)
	}
}

func TestResync_ConnectionNotFound(t *testing.T) {
	conns := &fakeConns{byID: map[string]*whoopconn.Connection{}}
	svc := &fakeResyncer{}
	h := whoopadmin.NewHandler(conns, &fakeRecovery{}, svc, fixedNow("2026-07-31T12:00:00Z"))
	srv := newRouter(h)
	resp := postResync(t, srv, map[string]any{"user_id": "ghost", "window_days": 30})
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", resp.Code, resp.Body.String())
	}
	if svc.callCount != 0 {
		t.Errorf("SyncSince called %d times, want 0", svc.callCount)
	}
}

func TestResync_StatusNotConnected(t *testing.T) {
	conn := &whoopconn.Connection{UserID: "userA", Status: whoopconn.StatusRevoked}
	conns := &fakeConns{byID: map[string]*whoopconn.Connection{"userA": conn}}
	svc := &fakeResyncer{}
	h := whoopadmin.NewHandler(conns, &fakeRecovery{}, svc, fixedNow("2026-07-31T12:00:00Z"))
	srv := newRouter(h)
	resp := postResync(t, srv, map[string]any{"user_id": "userA", "window_days": 30})
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", resp.Code, resp.Body.String())
	}
	env := decodeEnvelope(t, resp.Body.Bytes())
	if !bytes.Contains([]byte(env.Error), []byte(string(whoopconn.StatusRevoked))) {
		t.Errorf("error %q does not mention status %q", env.Error, whoopconn.StatusRevoked)
	}
	if svc.callCount != 0 {
		t.Errorf("SyncSince called %d times, want 0", svc.callCount)
	}
}

func TestResync_ReconnectNeeded(t *testing.T) {
	h, _ := resyncFixture(whoopsync.SyncResult{}, whoopsync.ErrReconnectNeeded)
	srv := newRouter(h)
	resp := postResync(t, srv, map[string]any{"user_id": "userA", "window_days": 30})
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", resp.Code, resp.Body.String())
	}
}

func TestResync_Upstream(t *testing.T) {
	wrapped := fmt.Errorf("fetch recoveries: %w", whoopsync.ErrUpstream)
	h, _ := resyncFixture(whoopsync.SyncResult{}, wrapped)
	srv := newRouter(h)
	resp := postResync(t, srv, map[string]any{"user_id": "userA", "window_days": 30})
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", resp.Code, resp.Body.String())
	}
}

func TestResync_GenericError(t *testing.T) {
	h, _ := resyncFixture(whoopsync.SyncResult{}, errors.New("boom"))
	srv := newRouter(h)
	resp := postResync(t, srv, map[string]any{"user_id": "userA", "window_days": 30})
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", resp.Code, resp.Body.String())
	}
}
