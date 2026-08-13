package calendarsync

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
	"golang.org/x/oauth2"

	"github.com/Prog-Strength/prog-strength-api/internal/auth"
	"github.com/Prog-Strength/prog-strength-api/internal/calendarconn"
	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
	"github.com/Prog-Strength/prog-strength-api/internal/tokencrypt"
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

// --- GET /me/calendar/events ---

// stubEventsClient is a CalendarClient serving a fixed list. These tests own
// the HTTP contract of our endpoint, not Google's wire format — the events
// service's own tests drive the REAL client against an httptest server, so a
// fake here costs no coverage of the parsing it would otherwise hide.
type stubEventsClient struct {
	events   []ListedEvent
	timezone string // the calendar's zone, as events.list reports it
	err      error
}

func (s *stubEventsClient) InsertEvent(context.Context, string, string, GoogleEvent) (string, error) {
	return "", errors.New("unused")
}

func (s *stubEventsClient) PatchEvent(context.Context, string, string, string, GoogleEvent) error {
	return errors.New("unused")
}

func (s *stubEventsClient) DeleteEvent(context.Context, string, string, string) error {
	return errors.New("unused")
}

func (s *stubEventsClient) ListEvents(context.Context, string, string, time.Time, time.Time, int) ([]ListedEvent, string, error) {
	return s.events, s.timezone, s.err
}

// newEventsHandler builds a handler with an events service attached. Each id in
// connectedUsers gets a connected calendar connection row; pass none for a user
// who never opted in.
func newEventsHandler(t *testing.T, client CalendarClient, connectedUsers ...string) *Handler {
	t.Helper()

	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	cipher := testCipher(t)
	enc, nonce, err := cipher.Encrypt([]byte("refresh-token"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	for _, userID := range connectedUsers {
		if upErr := conns.Upsert(context.Background(), userID, enc, nonce, "primary", CalendarEventsScope, eventsNow); upErr != nil {
			t.Fatalf("Upsert conn %s: %v", userID, upErr)
		}
	}

	svc := NewEventsService(EventsServiceDeps{
		Conns:  conns,
		Cipher: cipher,
		Client: client,
		Links:  &stubLinks{},
		Config: defaultEventsConfig(),
		Now:    func() time.Time { return eventsNow },
	})
	svc.conn.tokens = fakeTokens{} // inject the fake token minter directly

	h := newHandler(t, conns, "")
	h.AttachEvents(svc)
	return h
}

// eventsBody decodes the endpoint's data envelope. days is a pointer so a test
// can tell an ABSENT key from an empty or null array — the contract turns on
// that distinction.
type eventsBody struct {
	Status   string `json:"status"`
	Timezone string `json:"timezone"`
	Days     *[]struct {
		Date      string `json:"date"`
		Truncated int    `json:"truncated"`
		Events    *[]struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Start  string `json:"start"`
			End    string `json:"end"`
			AllDay bool   `json:"all_day"`
			Source string `json:"source"`
			Link   *struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"link"`
		} `json:"events"`
	} `json:"days"`
}

func decodeEvents(t *testing.T, rec *httptest.ResponseRecorder) eventsBody {
	t.Helper()
	var env struct {
		Data eventsBody `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, rec.Body.String())
	}
	return env.Data
}

// errorMessage pulls the `error` field out of the failure envelope.
func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v; body=%s", err, rec.Body.String())
	}
	return env.Error
}

func TestGetEvents_RequiresTimezone(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}, "user-1"), "user-1")

	rec := doGet(router, "/me/calendar/events?start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// daterange's message verbatim: handlers forward err.Error(), so the string
	// is the contract.
	if got := errorMessage(t, rec); got != "timezone is required" {
		t.Errorf("error = %q, want %q", got, "timezone is required")
	}
}

func TestGetEvents_RejectsUnknownTimezone(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}, "user-1"), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=Mars/Olympus&start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec); !strings.Contains(got, "invalid timezone") {
		t.Errorf("error = %q, want it to mention the invalid timezone", got)
	}
}

func TestGetEvents_RequiresBothDates(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}, "user-1"), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=UTC&start_date=2026-08-10")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	want := "end_date is required when start_date is supplied"
	if got := errorMessage(t, rec); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// TestGetEvents_RejectsTheSingleDateForm pins the one place this endpoint is
// STRICTER than daterange: `?date=` parses fine, but the tile always asks for a
// range, and a lone date would leave the window's raw bounds empty.
func TestGetEvents_RejectsTheSingleDateForm(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}, "user-1"), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=UTC&date=2026-08-10")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec); !strings.Contains(got, "start_date") {
		t.Errorf("error = %q, want it to name start_date", got)
	}
}

func TestGetEvents_RejectsAnOversizeWindow(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}, "user-1"), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=UTC&start_date=2026-08-10&end_date=2026-11-08")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec); !strings.Contains(got, "31") {
		t.Errorf("error = %q, want it to mention the 31-day cap", got)
	}
}

// The REJECTING side of the boundary, one day past the limit. Without it the
// cap is only pinned from below — 31 accepted and 91 rejected leave every
// off-by-one in between (32, say) passing the suite.
func TestGetEvents_RejectsAWindowOneDayOverTheCap(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}, "user-1"), "user-1")

	// 2026-08-10 through 2026-09-10 inclusive is 32 local days.
	rec := doGet(router, "/me/calendar/events?timezone=UTC&start_date=2026-08-10&end_date=2026-09-10")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a 32-day window; body=%s", rec.Code, rec.Body.String())
	}
	if got := errorMessage(t, rec); !strings.Contains(got, "31") {
		t.Errorf("error = %q, want it to mention the 31-day cap", got)
	}
}

// TestGetEvents_AcceptsTheMaximumWindowAcrossDST pins the cap's boundary AND
// the arithmetic behind it. This window is exactly 31 INCLUSIVE local days, but
// it spans Denver's fall-back, so it is 745 hours long — a cap that subtracted
// two instants and compared against 31*24h would reject a legal window twice a
// year, for the users least able to explain why.
func TestGetEvents_AcceptsTheMaximumWindowAcrossDST(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}, "user-1"), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=America/Denver&start_date=2025-10-20&end_date=2025-11-19")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeEvents(t, rec)
	if body.Status != string(EventsStatusOK) {
		t.Fatalf("status = %q, want ok", body.Status)
	}
	if body.Days == nil || len(*body.Days) != 31 {
		t.Errorf("days = %v, want 31 entries", body.Days)
	}
}

// TestGetEvents_NotConnectedIs200 is the endpoint's central product decision. A
// user who never opted in is not an error: the tile's job there is to invite,
// and a 404 would make every client branch on a status code to render a CTA.
func TestGetEvents_NotConnectedIs200(t *testing.T) {
	// No connected users — the connection row simply does not exist.
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=UTC&start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeEvents(t, rec)
	if body.Status != string(EventsStatusNotConnected) {
		t.Errorf("status = %q, want not_connected", body.Status)
	}
	// Nothing to say beyond the status: `days` must be ABSENT, not null.
	if body.Days != nil {
		t.Errorf("days = %v, want the key omitted on a degraded status", *body.Days)
	}
	if strings.Contains(rec.Body.String(), `"days"`) {
		t.Errorf("body %s must not carry a days key", rec.Body.String())
	}
}

// TestGetEvents_UnattachedServiceIsUnavailable covers the deploy where the
// reader was never wired: the connection routes still work, and the endpoint
// degrades rather than panicking on a nil service.
func TestGetEvents_UnattachedServiceIsUnavailable(t *testing.T) {
	conns := calendarconn.NewSQLiteRepository(dbtest.New(t))
	router := authedRouter(newHandler(t, conns, ""), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=UTC&start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeEvents(t, rec)
	if body.Status != string(EventsStatusUnavailable) {
		t.Errorf("status = %q, want unavailable", body.Status)
	}
	if body.Days != nil {
		t.Errorf("days = %v, want the key omitted", *body.Days)
	}
}

func TestGetEvents_OKShapeIsDenseAndTyped(t *testing.T) {
	client := &stubEventsClient{events: []ListedEvent{{
		ID:      "abc123",
		Summary: "Upper Body Push",
		Start:   time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
	}}}
	router := authedRouter(newEventsHandler(t, client, "user-1"), "user-1")

	rec := doGet(router, "/me/calendar/events?timezone=UTC&start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeEvents(t, rec)
	if body.Status != string(EventsStatusOK) {
		t.Fatalf("status = %q, want ok", body.Status)
	}
	if body.Days == nil {
		t.Fatal("days is absent or null on an ok response")
	}
	days := *body.Days

	// Dense: one entry per date in the window, in order, even for a free day.
	wantDates := []string{"2026-08-10", "2026-08-11", "2026-08-12"}
	if len(days) != len(wantDates) {
		t.Fatalf("days = %d entries, want %d", len(days), len(wantDates))
	}
	for i, want := range wantDates {
		if days[i].Date != want {
			t.Errorf("days[%d].date = %q, want %q", i, days[i].Date, want)
		}
		if days[i].Events == nil {
			t.Fatalf("days[%d].events is null; the array must be present on every day", i)
		}
		if days[i].Truncated != 0 {
			t.Errorf("days[%d].truncated = %d, want 0", i, days[i].Truncated)
		}
	}
	if got := len(*days[0].Events); got != 0 {
		t.Errorf("free day has %d events, want 0", got)
	}
	if got := len(*days[2].Events); got != 0 {
		t.Errorf("free day has %d events, want 0", got)
	}

	populated := *days[1].Events
	if len(populated) != 1 {
		t.Fatalf("day 2 has %d events, want 1", len(populated))
	}
	ev := populated[0]
	if ev.ID != "abc123" {
		t.Errorf("id = %q, want abc123", ev.ID)
	}
	if ev.Title != "Upper Body Push" {
		t.Errorf("title = %q, want Upper Body Push", ev.Title)
	}
	if ev.AllDay {
		t.Error("all_day = true, want false for a timed event")
	}
	if ev.Source != EventSourceGoogle {
		t.Errorf("source = %q, want google", ev.Source)
	}
	if ev.Link != nil {
		t.Errorf("link = %+v, want it omitted for a google event", ev.Link)
	}
	// RFC3339 UTC on the wire, so a client never has to guess an offset.
	if ev.Start != "2026-08-11T11:00:00Z" {
		t.Errorf("start = %q, want 2026-08-11T11:00:00Z", ev.Start)
	}
	if ev.End != "2026-08-11T12:00:00Z" {
		t.Errorf("end = %q, want 2026-08-11T12:00:00Z", ev.End)
	}
}

func TestGetEvents_NoAuthIs401(t *testing.T) {
	h := newEventsHandler(t, &stubEventsClient{}, "user-1")
	// Mount authed routes WITHOUT the user-injecting middleware.
	r := chi.NewRouter()
	h.MountAuthed(r)

	rec := doGet(r, "/me/calendar/events?timezone=UTC&start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
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

// TestGetEvents_NamesTheZoneItsClocksAreIn pins the wire half of the fix. The
// instants are UTC and always were; without the zone beside them a client has
// nothing to read them on but its own, which is how a 4:45 PM Eastern flight
// reached a Pacific browser as 1:45 PM.
func TestGetEvents_NamesTheZoneItsClocksAreIn(t *testing.T) {
	client := &stubEventsClient{
		timezone: "America/New_York",
		events: []ListedEvent{{
			ID:      "flight",
			Summary: "Southwest",
			Start:   time.Date(2026, 8, 11, 20, 45, 0, 0, time.UTC), // 4:45 PM Eastern
			End:     time.Date(2026, 8, 11, 23, 45, 0, 0, time.UTC),
		}},
	}
	router := authedRouter(newEventsHandler(t, client, "user-1"), "user-1")

	// The CALLER asks in Pacific — the browser's zone, which decides which days
	// were requested and nothing else.
	rec := doGet(router, "/me/calendar/events?timezone=America/Los_Angeles&start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeEvents(t, rec)
	if body.Timezone != "America/New_York" {
		t.Errorf("timezone = %q, want America/New_York — the calendar's zone, not the caller's", body.Timezone)
	}
	if body.Days == nil {
		t.Fatal("days is absent on an ok response")
	}
	for _, d := range *body.Days {
		if d.Events == nil {
			continue
		}
		for _, e := range *d.Events {
			if e.ID != "flight" {
				continue
			}
			// The instant is untouched; only the zone to read it on is new.
			if e.Start != "2026-08-11T20:45:00Z" {
				t.Errorf("start = %q, want 2026-08-11T20:45:00Z", e.Start)
			}
			return
		}
	}
	t.Error("the flight is missing from every day in the window")
}

// TestGetEvents_OmitsTheZoneWhenDegraded keeps the degraded response down to
// just its status, which is the shape every other failure already has.
func TestGetEvents_OmitsTheZoneWhenDegraded(t *testing.T) {
	router := authedRouter(newEventsHandler(t, &stubEventsClient{}), "user-1") // no connection
	rec := doGet(router, "/me/calendar/events?timezone=UTC&start_date=2026-08-10&end_date=2026-08-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if body := decodeEvents(t, rec); body.Timezone != "" {
		t.Errorf("timezone = %q, want it absent on a degraded response", body.Timezone)
	}
}
