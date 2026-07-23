package calendarsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/calendarconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/tokencrypt"
)

// testCipher builds a deterministic AES-256 cipher for tests.
func testCipher(t *testing.T) *tokencrypt.Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := tokencrypt.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

// newHandler wires a handler against an in-memory repo and a config whose token
// endpoint points at the given test server (empty tokenURL keeps the real
// Google endpoint, fine for tests that never exchange).
func newHandler(t *testing.T, conns calendarconn.Repository, tokenURL string) *Handler {
	t.Helper()
	cfg := NewCalendarConfig("client-id", "secret", "https://api.example.com/auth/google/calendar/callback")
	if tokenURL != "" {
		cfg.Endpoint = oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: tokenURL,
		}
	}
	return NewHandler(cfg, conns, testCipher(t), http.DefaultClient, []string{"https://app.example.com"}, testHMACKey)
}

// authedRouter mounts the authed routes behind a middleware that injects the
// given user id, simulating auth.RequireUser without a real JWT.
func authedRouter(h *Handler, userID string) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := auth.WithUserID(req.Context(), userID)
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		h.MountAuthed(r)
	})
	return r
}

// publicRouter mounts only the public callback route.
func publicRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.MountPublic(r)
	return r
}

func TestConnectRedirectsToGoogle(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	router := authedRouter(h, "user-1")

	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/connect", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}

	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	q := u.Query()
	if !strings.Contains(q.Get("scope"), CalendarEventsScope) {
		t.Errorf("scope = %q, want calendar.events", q.Get("scope"))
	}
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline", q.Get("access_type"))
	}
	if q.Get("prompt") != "consent" {
		t.Errorf("prompt = %q, want consent", q.Get("prompt"))
	}
	if q.Get("include_granted_scopes") != "true" {
		t.Errorf("include_granted_scopes = %q, want true", q.Get("include_granted_scopes"))
	}

	// The state cookie must be set, and the state param must encode user-1.
	cookie := findCookie(rec.Result().Cookies(), stateCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("state cookie not set")
	}
	random, userID, err := decodeState(q.Get("state"), testHMACKey)
	if err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if userID != "user-1" {
		t.Errorf("state userID = %q, want user-1", userID)
	}
	if random != cookie.Value {
		t.Errorf("state random %q != cookie %q", random, cookie.Value)
	}
}

func TestConnectRequiresUser(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	// Mount authed routes WITHOUT the user-injecting middleware.
	r := chi.NewRouter()
	h.MountAuthed(r)

	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/connect", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCallbackStoresEncryptedConnection(t *testing.T) {
	tokenSrv := newTokenServer(t, map[string]any{
		"access_token":  "at-1",
		"refresh_token": "rt-original-secret",
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
	defer tokenSrv.Close()

	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, tokenSrv.URL)
	h.httpClient = tokenSrv.Client()
	router := publicRouter(h)

	random := "csrf-random"
	state := encodeState(random, "user-7", testHMACKey)
	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: random})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	conn, err := conns.Get(context.Background(), "user-7")
	if err != nil {
		t.Fatalf("Get connection: %v", err)
	}
	if conn.Status != calendarconn.StatusConnected {
		t.Errorf("status = %q, want connected", conn.Status)
	}
	if conn.GoogleCalendarID != defaultCalendarID {
		t.Errorf("calendar id = %q, want %q", conn.GoogleCalendarID, defaultCalendarID)
	}

	enc, nonce, err := conns.GetRefreshToken(context.Background(), "user-7")
	if err != nil {
		t.Fatalf("GetRefreshToken: %v", err)
	}
	plain, err := h.cipher.Decrypt(enc, nonce)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plain) != "rt-original-secret" {
		t.Errorf("decrypted token = %q, want rt-original-secret", plain)
	}
}

func TestCallbackRedirectsToReturnTo(t *testing.T) {
	tokenSrv := newTokenServer(t, map[string]any{
		"access_token":  "at-1",
		"refresh_token": "rt-x",
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
	defer tokenSrv.Close()

	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, tokenSrv.URL)
	h.httpClient = tokenSrv.Client()
	router := publicRouter(h)

	random := "csrf-random"
	state := encodeState(random, "user-9", testHMACKey)
	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/callback?code=c&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: random})
	req.AddCookie(&http.Cookie{Name: returnToCookieName, Value: "https://app.example.com/settings"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://app.example.com/settings#") {
		t.Errorf("location = %q, want redirect to return_to", loc)
	}
	if !strings.Contains(loc, "calendar=connected") {
		t.Errorf("location = %q, want calendar=connected fragment", loc)
	}
}

func TestCallbackMismatchedState(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	router := publicRouter(h)

	state := encodeState("real-random", "user-1", testHMACKey)
	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/callback?code=c&state="+url.QueryEscape(state), nil)
	// Cookie carries a DIFFERENT random.
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "different-random"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ok, _ := conns.Exists(context.Background(), "user-1"); ok {
		t.Error("connection row was written despite bad state")
	}
}

// TestCallbackRejectsForgedStateAccountLinking is the regression test for the
// account-linking CSRF: an attacker who completed a real Google consent for
// their own account replays the callback with a state that carries the VICTIM's
// userID and an attacker-chosen random (also set as the matching cookie). The
// random matches the cookie, so the old code accepted it and stored the
// attacker's refresh token under the victim. With HMAC-signed state the forged
// state has no valid signature (the attacker doesn't know the server secret),
// so the callback must reject it with 400 and write NO connection row.
func TestCallbackRejectsForgedStateAccountLinking(t *testing.T) {
	// Token server would hand back the attacker's refresh token if we ever got
	// far enough to exchange — we must NOT.
	tokenSrv := newTokenServer(t, map[string]any{
		"access_token":  "attacker-at",
		"refresh_token": "attacker-rt",
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
	defer tokenSrv.Close()

	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, tokenSrv.URL)
	h.httpClient = tokenSrv.Client()
	router := publicRouter(h)

	const victimUserID = "victim-user"
	attackerRandom := "attacker-chosen-random"

	// The attacker forges state for the victim, signing with a key they control
	// (they don't know the server's real stateHMACKey).
	forgedState := encodeState(attackerRandom, victimUserID, []byte("attacker-key"))

	req := httptest.NewRequest(http.MethodGet,
		"/auth/google/calendar/callback?code=attacker-code&state="+url.QueryEscape(forgedState), nil)
	// Cookie matches the attacker's random, so the random==cookie check passes;
	// only the signature stops the attack.
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: attackerRandom})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (forged state must be rejected); body=%s", rec.Code, rec.Body.String())
	}
	if ok, _ := conns.Exists(context.Background(), victimUserID); ok {
		t.Fatal("attacker linked their calendar to the victim: connection row was written")
	}
}

func TestCallbackMissingStateCookie(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	router := publicRouter(h)

	state := encodeState("r", "user-1", testHMACKey)
	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/callback?code=c&state="+url.QueryEscape(state), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCallbackNoRefreshToken(t *testing.T) {
	tokenSrv := newTokenServer(t, map[string]any{
		"access_token": "at-1",
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
	defer tokenSrv.Close()

	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, tokenSrv.URL)
	h.httpClient = tokenSrv.Client()
	router := publicRouter(h)

	random := "csrf-random"
	state := encodeState(random, "user-3", testHMACKey)
	req := httptest.NewRequest(http.MethodGet, "/auth/google/calendar/callback?code=c&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: random})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// No row must be written.
	if ok, _ := conns.Exists(context.Background(), "user-3"); ok {
		t.Error("connection row was written despite missing refresh token")
	}
	// Error body must carry the machine-readable code.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["code"] != "no_refresh_token" {
		t.Errorf("code = %v, want no_refresh_token", body["code"])
	}
}

func TestGetConnectionAbsent(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	router := authedRouter(h, "user-1")

	rec := doGet(router, "/me/calendar/connection")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := connStatus(t, rec); got != "absent" {
		t.Errorf("status = %q, want absent", got)
	}
}

func TestGetConnectionConnected(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	if err := conns.Upsert(context.Background(), "user-1", []byte("e"), []byte("n"), "primary", CalendarEventsScope, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	h := newHandler(t, conns, "")
	router := authedRouter(h, "user-1")

	rec := doGet(router, "/me/calendar/connection")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := connStatus(t, rec); got != "connected" {
		t.Errorf("status = %q, want connected", got)
	}
}

func TestGetConnectionRevoked(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	if err := conns.Upsert(context.Background(), "user-1", []byte("e"), []byte("n"), "primary", CalendarEventsScope, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := conns.SetStatus(context.Background(), "user-1", calendarconn.StatusRevoked, time.Now()); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	h := newHandler(t, conns, "")
	router := authedRouter(h, "user-1")

	rec := doGet(router, "/me/calendar/connection")
	if got := connStatus(t, rec); got != "revoked" {
		t.Errorf("status = %q, want revoked", got)
	}
}

func TestDeleteConnectionRevokesAndDeletes(t *testing.T) {
	// A fake revoke endpoint records that it was called.
	var revokeCalled bool
	var revokedToken string
	revokeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeCalled = true
		_ = r.ParseForm()
		revokedToken = r.Form.Get("token")
		w.WriteHeader(http.StatusOK)
	}))
	defer revokeSrv.Close()

	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	h.httpClient = revokeSrv.Client()
	h.revokeURL = revokeSrv.URL

	// Store an encrypted token so delete can decrypt + revoke it.
	enc, nonce, err := h.cipher.Encrypt([]byte("rt-to-revoke"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := conns.Upsert(context.Background(), "user-1", enc, nonce, "primary", CalendarEventsScope, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	router := authedRouter(h, "user-1")
	rec := doDelete(router, "/me/calendar/connection")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !revokeCalled {
		t.Error("revoke endpoint was not called")
	}
	if revokedToken != "rt-to-revoke" {
		t.Errorf("revoked token = %q, want rt-to-revoke", revokedToken)
	}
	if ok, _ := conns.Exists(context.Background(), "user-1"); ok {
		t.Error("connection row still exists after delete")
	}
}

func TestDeleteConnectionRevokeFailureStillDeletes(t *testing.T) {
	// Revoke endpoint always 500s; delete must still proceed.
	revokeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer revokeSrv.Close()

	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	h.httpClient = revokeSrv.Client()
	h.revokeURL = revokeSrv.URL

	enc, nonce, _ := h.cipher.Encrypt([]byte("rt"))
	if err := conns.Upsert(context.Background(), "user-1", enc, nonce, "primary", CalendarEventsScope, time.Now()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	router := authedRouter(h, "user-1")
	rec := doDelete(router, "/me/calendar/connection")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when revoke fails; body=%s", rec.Code, rec.Body.String())
	}
	if ok, _ := conns.Exists(context.Background(), "user-1"); ok {
		t.Error("connection row still exists after delete despite revoke failure")
	}
}

func TestDeleteConnectionAbsent(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	h := newHandler(t, conns, "")
	router := authedRouter(h, "user-1")

	rec := doDelete(router, "/me/calendar/connection")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// --- shared test helpers ---

func doGet(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doDelete(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func connStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, rec.Body.String())
	}
	return env.Data.Status
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}
