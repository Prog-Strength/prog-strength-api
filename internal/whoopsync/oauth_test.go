package whoopsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

// testHMACKey is a fixed key used to sign/verify OAuth state in unit tests.
var testHMACKey = []byte("test-jwt-signing-key-for-state-hmac")

func encodeStateRaw(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestEncodeDecodeStateRoundTrip(t *testing.T) {
	random := "rnd-abc123"
	userID := "user-42"
	state := encodeState(random, userID, testHMACKey)

	gotRandom, gotUser, err := decodeState(state, testHMACKey)
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
		"not base64":        "!!!not-base64!!!",
		"no separators":     encodeStateRaw("nocolonhere"),
		"missing signature": encodeStateRaw("rnd:user"),
		"empty userID":      encodeStateRaw("rnd::sig"),
		"empty random":      encodeStateRaw(":user:sig"),
		"empty signature":   encodeStateRaw("rnd:user:"),
		"all empty":         encodeStateRaw("::"),
		"completely raw":    "",
	}
	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeState(state, testHMACKey); err == nil {
				t.Errorf("decodeState(%q) = nil err, want error", state)
			}
		})
	}
}

// TestDecodeStateRejectsTamperedSignature proves a syntactically valid state
// whose signature doesn't match the key (forged userID, swapped key, or flipped
// bits) is rejected — the core of the account-linking CSRF fix.
func TestDecodeStateRejectsTamperedSignature(t *testing.T) {
	forged := encodeState("attacker-random", "victim-user", []byte("attacker-guessed-key"))
	if _, _, err := decodeState(forged, testHMACKey); err == nil {
		t.Error("decodeState accepted a state signed with the wrong key")
	}

	validSig := encodeState("r", "real-user", testHMACKey)
	rawValid, _ := base64.RawURLEncoding.DecodeString(validSig)
	parts := strings.SplitN(string(rawValid), ":", 3)
	tampered := encodeStateRaw("r:attacker-user:" + parts[2])
	if _, _, err := decodeState(tampered, testHMACKey); err == nil {
		t.Error("decodeState accepted a state whose userID was swapped after signing")
	}
}

// TestDecodeStateTruncated proves a truncated (byte-dropped) signature fails.
func TestDecodeStateTruncated(t *testing.T) {
	state := encodeState("rnd", "user", testHMACKey)
	if _, _, err := decodeState(state[:len(state)-4], testHMACKey); err == nil {
		t.Error("decodeState accepted a truncated state")
	}
}

// testOAuthConfig builds an OAuthConfig whose token endpoint points at a test
// server, so Exchange/Refresh never hit WHOOP.
func testOAuthConfig(tokenURL string) *OAuthConfig {
	c := NewOAuthConfig("client-id", "secret", "https://api.example.com/cb")
	c.cfg.Endpoint = oauth2.Endpoint{
		AuthURL:  authorizeURL,
		TokenURL: tokenURL,
	}
	return c
}

func newTokenServer(t *testing.T, status int, body map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestRandomTokenIsUniqueAndURLSafe(t *testing.T) {
	a, err := randomToken()
	if err != nil {
		t.Fatalf("randomToken: %v", err)
	}
	b, err := randomToken()
	if err != nil {
		t.Fatalf("randomToken: %v", err)
	}
	if a == "" || a == b {
		t.Errorf("randomToken not unique/non-empty: %q %q", a, b)
	}
	if _, err := base64.RawURLEncoding.DecodeString(a); err != nil {
		t.Errorf("randomToken not url-safe base64: %v", err)
	}
}

func TestAuthCodeURLParams(t *testing.T) {
	c := testOAuthConfig("https://tok.example.com")
	raw := c.AuthCodeURL(encodeState("rnd", "user-1", testHMACKey))
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != "client-id" {
		t.Errorf("client_id = %q, want client-id", got)
	}
	if got := q.Get("scope"); !strings.Contains(got, "read:recovery") {
		t.Errorf("scope = %q, want to contain read:recovery", got)
	}
	if !strings.HasPrefix(raw, authorizeURL) {
		t.Errorf("auth url = %q, want prefix %q", raw, authorizeURL)
	}
}

func TestExchangeReturnsTokens(t *testing.T) {
	srv := newTokenServer(t, http.StatusOK, map[string]any{
		"access_token":  "at-1",
		"refresh_token": "rt-secret",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         ScopeString,
	})
	defer srv.Close()

	c := testOAuthConfig(srv.URL)
	tok, err := c.Exchange(context.Background(), srv.Client(), "code-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "at-1" {
		t.Errorf("access token = %q, want at-1", tok.AccessToken)
	}
	if tok.RefreshToken != "rt-secret" {
		t.Errorf("refresh token = %q, want rt-secret", tok.RefreshToken)
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero, want a future expiry")
	}
}

func TestExchangeNoRefreshToken(t *testing.T) {
	srv := newTokenServer(t, http.StatusOK, map[string]any{
		"access_token": "at-1",
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
	defer srv.Close()

	c := testOAuthConfig(srv.URL)
	_, err := c.Exchange(context.Background(), srv.Client(), "code-1")
	if !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("err = %v, want ErrNoRefreshToken", err)
	}
}

func TestRefreshRotatesRefreshToken(t *testing.T) {
	var gotGrant, gotRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		gotRefresh = r.Form.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-new",
			"refresh_token": "rt-rotated",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         ScopeString,
		})
	}))
	defer srv.Close()

	c := testOAuthConfig(srv.URL)
	tok, err := c.Refresh(context.Background(), srv.Client(), "rt-old")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if gotGrant != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", gotGrant)
	}
	if gotRefresh != "rt-old" {
		t.Errorf("sent refresh_token = %q, want rt-old", gotRefresh)
	}
	if tok.AccessToken != "at-new" {
		t.Errorf("access token = %q, want at-new", tok.AccessToken)
	}
	if tok.RefreshToken != "rt-rotated" {
		t.Errorf("refresh token = %q, want rt-rotated (rotated)", tok.RefreshToken)
	}
}

func TestRefreshInvalidGrant(t *testing.T) {
	srv := newTokenServer(t, http.StatusBadRequest, map[string]any{
		"error": "invalid_grant",
	})
	defer srv.Close()

	c := testOAuthConfig(srv.URL)
	_, err := c.Refresh(context.Background(), srv.Client(), "rt-dead")
	if !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("err = %v, want ErrInvalidGrant", err)
	}
}

func TestRevoke(t *testing.T) {
	var gotMethod, gotAuth string
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	old := whoopRevokeURL
	whoopRevokeURL = srv.URL
	defer func() { whoopRevokeURL = old }()

	if err := Revoke(context.Background(), srv.Client(), "at-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !called {
		t.Fatal("revoke endpoint was not called")
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	if gotAuth != "Bearer at-1" {
		t.Errorf("auth = %q, want Bearer at-1", gotAuth)
	}
}

func TestRevokeNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	old := whoopRevokeURL
	whoopRevokeURL = srv.URL
	defer func() { whoopRevokeURL = old }()

	if err := Revoke(context.Background(), srv.Client(), "at"); err == nil {
		t.Error("Revoke: want error on non-2xx, got nil")
	}
}
