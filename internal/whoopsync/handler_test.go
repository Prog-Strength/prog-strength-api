package whoopsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db/dbtest"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/tokencrypt"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
)

// --- handler harness --------------------------------------------------------

// handlerCipher builds a deterministic AES-256 cipher for handler tests.
func handlerCipher(t *testing.T) *tokencrypt.Cipher {
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

// handlerDeps bundles the repos + cipher a handler test wires together.
type handlerDeps struct {
	conns  whoopconn.Repository
	rec    whooprecovery.Repository
	cipher *tokencrypt.Cipher
}

func newHandlerDeps(t *testing.T) handlerDeps {
	t.Helper()
	database := dbtest.New(t)
	return handlerDeps{
		conns:  whoopconn.NewSQLiteRepository(database),
		rec:    whooprecovery.NewSQLiteRepository(database),
		cipher: handlerCipher(t),
	}
}

// newTestHandler wires a Handler whose OAuth token endpoint points at tokenURL
// (empty keeps the real WHOOP endpoint, fine for tests that never exchange) and
// whose backfill service uses api (nil → a fake that returns no data). The
// httpClient is http.DefaultClient by default; callers override it to target the
// httptest token/profile/revoke servers.
func newTestHandler(t *testing.T, d handlerDeps, tokenURL string, api whoopAPI) *Handler {
	t.Helper()
	oauth := testOAuthConfig(tokenURL)
	if api == nil {
		api = &fakeAPI{}
	}
	svc := NewService(d.conns, d.rec, d.cipher, api, oauth, http.DefaultClient, nil)
	client := NewClient(http.DefaultClient)
	return NewHandler(oauth, client, d.conns, svc, d.cipher, http.DefaultClient,
		[]string{"https://app.example.com"}, testHMACKey, nil)
}

// hAuthedRouter mounts the authed routes behind a middleware that injects the
// given user id, simulating auth.RequireUser without a real JWT.
func hAuthedRouter(h *Handler, userID string) http.Handler {
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

// hPublicRouter mounts only the public callback route.
func hPublicRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.MountPublic(r)
	return r
}

// profileServer serves GET /v2/user/profile/basic with the given user id and
// repoints whoopAPIBase at itself for the test's duration.
func profileServer(t *testing.T, whoopUserID int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":    whoopUserID,
			"email":      "u@example.com",
			"first_name": "Test",
			"last_name":  "User",
		})
	}))
	old := whoopAPIBase
	whoopAPIBase = srv.URL
	t.Cleanup(func() { whoopAPIBase = old })
	return srv
}

// erroringAPI is a whoopAPI whose calls always fail, used to force Backfill to
// return an error (which the callback must swallow).
type erroringAPI struct{}

func (erroringAPI) Recoveries(context.Context, string, time.Time, time.Time, int) ([]Recovery, error) {
	return nil, errors.New("whoop api boom")
}
func (erroringAPI) Cycles(context.Context, string, time.Time, time.Time, int) ([]Cycle, error) {
	return nil, errors.New("whoop api boom")
}

// --- connect ----------------------------------------------------------------

func TestConnectRedirectsToWhoop(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	req := httptest.NewRequest(http.MethodGet, "/auth/whoop/connect", nil)
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
	if u.Host != "api.prod.whoop.com" {
		t.Errorf("host = %q, want api.prod.whoop.com", u.Host)
	}
	q := u.Query()
	if q.Get("state") == "" {
		t.Error("state param missing from redirect")
	}

	cookie := hFindCookie(rec.Result().Cookies(), stateCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("state cookie not set")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie flags = HttpOnly:%v SameSite:%v, want HttpOnly + Lax", cookie.HttpOnly, cookie.SameSite)
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

func TestConnectRejectsDisallowedReturnTo(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	req := httptest.NewRequest(http.MethodGet,
		"/auth/whoop/connect?return_to="+url.QueryEscape("https://evil.example.com/x"), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for disallowed return_to", rec.Code)
	}
}

func TestConnectRequiresUser(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	// Mount authed routes WITHOUT the user-injecting middleware.
	r := chi.NewRouter()
	h.MountAuthed(r)

	req := httptest.NewRequest(http.MethodGet, "/auth/whoop/connect", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// --- callback ---------------------------------------------------------------

func TestCallbackStoresEncryptedConnection(t *testing.T) {
	tokenSrv := newTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "at-secret",
		"refresh_token": "rt-secret",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         ScopeString,
	})
	defer tokenSrv.Close()
	profSrv := profileServer(t, 987654)
	defer profSrv.Close()

	d := newHandlerDeps(t)
	h := newTestHandler(t, d, tokenSrv.URL, nil)
	h.httpClient = tokenSrv.Client()
	h.client = NewClient(profSrv.Client())
	router := hPublicRouter(h)

	random := "csrf-random"
	state := encodeState(random, "user-7", testHMACKey)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/whoop/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: random})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	conn, err := d.conns.Get(context.Background(), "user-7")
	if err != nil {
		t.Fatalf("Get connection: %v", err)
	}
	if conn.Status != whoopconn.StatusConnected {
		t.Errorf("status = %q, want connected", conn.Status)
	}
	if conn.WhoopUserID != 987654 {
		t.Errorf("whoop_user_id = %d, want 987654", conn.WhoopUserID)
	}

	bundle, err := d.conns.GetTokens(context.Background(), "user-7")
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	access, err := d.cipher.Decrypt(bundle.AccessTokenEnc, bundle.AccessTokenNonce)
	if err != nil {
		t.Fatalf("decrypt access: %v", err)
	}
	if string(access) != "at-secret" {
		t.Errorf("decrypted access = %q, want at-secret", access)
	}
	refresh, err := d.cipher.Decrypt(bundle.RefreshTokenEnc, bundle.RefreshTokenNonce)
	if err != nil {
		t.Fatalf("decrypt refresh: %v", err)
	}
	if string(refresh) != "rt-secret" {
		t.Errorf("decrypted refresh = %q, want rt-secret", refresh)
	}
}

func TestCallbackRedirectsToReturnTo(t *testing.T) {
	tokenSrv := newTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "at",
		"refresh_token": "rt",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         ScopeString,
	})
	defer tokenSrv.Close()
	profSrv := profileServer(t, 111)
	defer profSrv.Close()

	d := newHandlerDeps(t)
	h := newTestHandler(t, d, tokenSrv.URL, nil)
	h.httpClient = tokenSrv.Client()
	h.client = NewClient(profSrv.Client())
	router := hPublicRouter(h)

	random := "csrf-random"
	state := encodeState(random, "user-9", testHMACKey)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/whoop/callback?code=c&state="+url.QueryEscape(state), nil)
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
	if !strings.Contains(loc, "whoop=connected") {
		t.Errorf("location = %q, want whoop=connected fragment", loc)
	}
}

func TestCallbackBackfillFailureStillConnects(t *testing.T) {
	tokenSrv := newTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "at",
		"refresh_token": "rt",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         ScopeString,
	})
	defer tokenSrv.Close()
	profSrv := profileServer(t, 222)
	defer profSrv.Close()

	d := newHandlerDeps(t)
	// Backfill's api always errors → Backfill returns an error the callback must
	// swallow.
	h := newTestHandler(t, d, tokenSrv.URL, erroringAPI{})
	h.httpClient = tokenSrv.Client()
	h.client = NewClient(profSrv.Client())
	router := hPublicRouter(h)

	random := "csrf-random"
	state := encodeState(random, "user-b", testHMACKey)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/whoop/callback?code=c&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: random})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite backfill failure; body=%s", rec.Code, rec.Body.String())
	}
	conn, err := d.conns.Get(context.Background(), "user-b")
	if err != nil {
		t.Fatalf("Get connection: %v", err)
	}
	if conn.Status != whoopconn.StatusConnected {
		t.Errorf("status = %q, want connected (backfill failure must not revoke)", conn.Status)
	}
}

func TestCallbackMismatchedState(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	router := hPublicRouter(h)

	state := encodeState("real-random", "user-1", testHMACKey)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/whoop/callback?code=c&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "different-random"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ok, _ := d.conns.Exists(context.Background(), "user-1"); ok {
		t.Error("connection row was written despite bad state")
	}
}

// TestCallbackRejectsForgedStateAccountLinking is the account-linking CSRF
// regression: an attacker forges state carrying the victim's userID signed with
// a key they control. The random matches their cookie, but the HMAC signature
// doesn't verify under the server key → 400, no row written.
func TestCallbackRejectsForgedStateAccountLinking(t *testing.T) {
	tokenSrv := newTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "attacker-at",
		"refresh_token": "attacker-rt",
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
	defer tokenSrv.Close()

	d := newHandlerDeps(t)
	h := newTestHandler(t, d, tokenSrv.URL, nil)
	h.httpClient = tokenSrv.Client()
	router := hPublicRouter(h)

	const victim = "victim-user"
	attackerRandom := "attacker-chosen-random"
	forged := encodeState(attackerRandom, victim, []byte("attacker-key"))

	req := httptest.NewRequest(http.MethodGet,
		"/auth/whoop/callback?code=attacker-code&state="+url.QueryEscape(forged), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: attackerRandom})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (forged state rejected); body=%s", rec.Code, rec.Body.String())
	}
	if ok, _ := d.conns.Exists(context.Background(), victim); ok {
		t.Fatal("attacker linked their whoop to the victim: connection row written")
	}
}

func TestCallbackNoRefreshToken(t *testing.T) {
	tokenSrv := newTokenServer(t, http.StatusOK, map[string]any{
		"access_token": "at",
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
	defer tokenSrv.Close()

	d := newHandlerDeps(t)
	h := newTestHandler(t, d, tokenSrv.URL, nil)
	h.httpClient = tokenSrv.Client()
	router := hPublicRouter(h)

	random := "csrf-random"
	state := encodeState(random, "user-3", testHMACKey)
	req := httptest.NewRequest(http.MethodGet,
		"/auth/whoop/callback?code=c&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: random})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if ok, _ := d.conns.Exists(context.Background(), "user-3"); ok {
		t.Error("connection row written despite missing refresh token")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["code"] != "no_refresh_token" {
		t.Errorf("code = %v, want no_refresh_token", body["code"])
	}
}

// --- getConnection ----------------------------------------------------------

func TestGetConnectionAbsent(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/me/whoop/connection")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := hConnStatus(t, rec); got != "absent" {
		t.Errorf("status = %q, want absent", got)
	}
}

func TestGetConnectionConnected(t *testing.T) {
	d := newHandlerDeps(t)
	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	bundle := whoopconn.TokenBundle{
		AccessTokenEnc: []byte("e"), AccessTokenNonce: []byte("n"),
		RefreshTokenEnc: []byte("e"), RefreshTokenNonce: []byte("n"),
		ExpiresAt: now.Add(time.Hour),
	}
	if err := d.conns.Upsert(context.Background(), "user-1", 42, bundle, ScopeString, now); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	h := newTestHandler(t, d, "", nil)
	router := hAuthedRouter(h, "user-1")

	rec := hDoGet(router, "/me/whoop/connection")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var env struct {
		Data struct {
			Status      string `json:"status"`
			ConnectedAt string `json:"connected_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if env.Data.Status != "connected" {
		t.Errorf("status = %q, want connected", env.Data.Status)
	}
	if env.Data.ConnectedAt == "" {
		t.Error("connected_at missing")
	}
	if _, err := time.Parse(time.RFC3339, env.Data.ConnectedAt); err != nil {
		t.Errorf("connected_at %q not RFC3339: %v", env.Data.ConnectedAt, err)
	}
}

// --- deleteConnection -------------------------------------------------------

func TestDeleteConnectionRevokesAndWipes(t *testing.T) {
	var revokeCalled bool
	var gotAuth string
	revokeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeCalled = true
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer revokeSrv.Close()
	oldRevoke := whoopRevokeURL
	whoopRevokeURL = revokeSrv.URL
	defer func() { whoopRevokeURL = oldRevoke }()

	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	h.httpClient = revokeSrv.Client()

	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	aEnc, aNonce, encErr := d.cipher.Encrypt([]byte("at-to-revoke"))
	if encErr != nil {
		t.Fatalf("encrypt: %v", encErr)
	}
	rEnc, rNonce, _ := d.cipher.Encrypt([]byte("rt"))
	bundle := whoopconn.TokenBundle{
		AccessTokenEnc: aEnc, AccessTokenNonce: aNonce,
		RefreshTokenEnc: rEnc, RefreshTokenNonce: rNonce,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := d.conns.Upsert(context.Background(), "user-1", 42, bundle, ScopeString, now); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	router := hAuthedRouter(h, "user-1")
	rec := hDoDelete(router, "/me/whoop/connection")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if !revokeCalled {
		t.Error("revoke endpoint was not called")
	}
	if gotAuth != "Bearer at-to-revoke" {
		t.Errorf("revoke auth = %q, want Bearer at-to-revoke", gotAuth)
	}

	conn, err := d.conns.Get(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if conn.Status != whoopconn.StatusRevoked {
		t.Errorf("status = %q, want revoked", conn.Status)
	}
	// Tokens must be wiped.
	tb, err := d.conns.GetTokens(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetTokens after revoke: %v", err)
	}
	if len(tb.AccessTokenEnc) != 0 || len(tb.RefreshTokenEnc) != 0 {
		t.Errorf("tokens not wiped: access=%d refresh=%d bytes", len(tb.AccessTokenEnc), len(tb.RefreshTokenEnc))
	}
}

func TestDeleteConnectionRevokeFailureStillRevokes(t *testing.T) {
	revokeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer revokeSrv.Close()
	oldRevoke := whoopRevokeURL
	whoopRevokeURL = revokeSrv.URL
	defer func() { whoopRevokeURL = oldRevoke }()

	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	h.httpClient = revokeSrv.Client()

	now := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	aEnc, aNonce, _ := d.cipher.Encrypt([]byte("at"))
	rEnc, rNonce, _ := d.cipher.Encrypt([]byte("rt"))
	bundle := whoopconn.TokenBundle{
		AccessTokenEnc: aEnc, AccessTokenNonce: aNonce,
		RefreshTokenEnc: rEnc, RefreshTokenNonce: rNonce,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := d.conns.Upsert(context.Background(), "user-1", 42, bundle, ScopeString, now); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	router := hAuthedRouter(h, "user-1")
	rec := hDoDelete(router, "/me/whoop/connection")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 even when revoke fails; body=%s", rec.Code, rec.Body.String())
	}
	conn, err := d.conns.Get(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if conn.Status != whoopconn.StatusRevoked {
		t.Errorf("status = %q, want revoked despite revoke failure", conn.Status)
	}
}

func TestDeleteConnectionAbsent(t *testing.T) {
	d := newHandlerDeps(t)
	h := newTestHandler(t, d, "", nil)
	h.httpClient = http.DefaultClient
	router := hAuthedRouter(h, "user-1")

	rec := hDoDelete(router, "/me/whoop/connection")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// --- shared helpers ---------------------------------------------------------

func hDoGet(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func hDoDelete(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func hConnStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
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

func hFindCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}
