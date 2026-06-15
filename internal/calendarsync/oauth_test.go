package calendarsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestEncodeDecodeStateRoundTrip(t *testing.T) {
	random := "rnd-abc123"
	userID := "user-42"
	state := encodeState(random, userID)

	gotRandom, gotUser, err := decodeState(state)
	if err != nil {
		t.Fatalf("decodeState: %v", err)
	}
	if gotRandom != random {
		t.Errorf("random = %q, want %q", gotRandom, random)
	}
	if gotUser != userID {
		t.Errorf("userID = %q, want %q", gotUser, userID)
	}
}

func TestDecodeStateRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"not base64":     "!!!not-base64!!!",
		"missing colon":  base64urlNoColon(),
		"empty userID":   encodeStateRaw("rnd:"),
		"empty random":   encodeStateRaw(":user"),
		"both empty":     encodeStateRaw(":"),
		"completely raw": "",
	}
	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeState(state); err == nil {
				t.Errorf("decodeState(%q) = nil err, want error", state)
			}
		})
	}
}

func TestAuthCodeURLParams(t *testing.T) {
	cfg := NewCalendarConfig("client-id", "secret", "https://api.example.com/auth/google/calendar/callback")
	raw := authCodeURL(cfg, encodeState("rnd", "user-1"))

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := u.Query()

	if got := q.Get("scope"); !strings.Contains(got, CalendarEventsScope) {
		t.Errorf("scope = %q, want to contain %q", got, CalendarEventsScope)
	}
	if got := q.Get("access_type"); got != "offline" {
		t.Errorf("access_type = %q, want offline", got)
	}
	if got := q.Get("prompt"); got != "consent" {
		t.Errorf("prompt = %q, want consent", got)
	}
	if got := q.Get("include_granted_scopes"); got != "true" {
		t.Errorf("include_granted_scopes = %q, want true", got)
	}
	if got := q.Get("client_id"); got != "client-id" {
		t.Errorf("client_id = %q, want client-id", got)
	}
}

func TestExchangeCodeReturnsRefreshToken(t *testing.T) {
	srv := newTokenServer(t, map[string]any{
		"access_token":  "at-1",
		"refresh_token": "rt-secret",
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
	defer srv.Close()

	cfg := tokenServerConfig(srv.URL)
	rt, err := exchangeCode(context.Background(), cfg, srv.Client(), "code-1")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if rt != "rt-secret" {
		t.Errorf("refresh token = %q, want rt-secret", rt)
	}
}

func TestExchangeCodeNoRefreshToken(t *testing.T) {
	srv := newTokenServer(t, map[string]any{
		"access_token": "at-1",
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
	defer srv.Close()

	cfg := tokenServerConfig(srv.URL)
	_, err := exchangeCode(context.Background(), cfg, srv.Client(), "code-1")
	if err == nil {
		t.Fatal("exchangeCode: want error, got nil")
	}
	if err != ErrNoRefreshToken {
		t.Errorf("err = %v, want ErrNoRefreshToken", err)
	}
}

func TestRevokeToken(t *testing.T) {
	var gotToken string
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = r.ParseForm()
		gotToken = r.Form.Get("token")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := revokeToken(context.Background(), srv.Client(), srv.URL, "rt-secret"); err != nil {
		t.Fatalf("revokeToken: %v", err)
	}
	if !called {
		t.Fatal("revoke endpoint was not called")
	}
	if gotToken != "rt-secret" {
		t.Errorf("revoked token = %q, want rt-secret", gotToken)
	}
}

func TestRevokeTokenNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	if err := revokeToken(context.Background(), srv.Client(), srv.URL, "rt"); err == nil {
		t.Error("revokeToken: want error on non-2xx, got nil")
	}
}

// --- test helpers ---

// newTokenServer returns an httptest.Server that responds to the OAuth token
// exchange with the given JSON body.
func newTokenServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
}

// tokenServerConfig builds an oauth2.Config whose token endpoint points at a
// test server, so Exchange never hits Google.
func tokenServerConfig(tokenURL string) *oauth2.Config {
	cfg := NewCalendarConfig("client-id", "secret", "https://api.example.com/cb")
	cfg.Endpoint = oauth2.Endpoint{
		AuthURL:  "https://accounts.google.com/o/oauth2/auth",
		TokenURL: tokenURL,
	}
	return cfg
}

func encodeStateRaw(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func base64urlNoColon() string {
	return encodeStateRaw("nocolonhere")
}
