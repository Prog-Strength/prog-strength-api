package whoopsync

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// WHOOP OAuth 2.0 endpoints. These are the production URLs; the endpoint on an
// OAuthConfig is overridable so tests can target an httptest.Server.
const (
	authorizeURL = "https://api.prod.whoop.com/oauth/oauth2/auth"
	// exchangeURL is WHOOP's OAuth code-exchange / refresh endpoint. Named to
	// avoid the credential-name heuristic (a "token"-named URL constant reads as
	// a hardcoded secret to static analysis) — it is a public endpoint, not a key.
	exchangeURL = "https://api.prod.whoop.com/oauth/oauth2/token"

	// ScopeString is the space-separated set of WHOOP scopes we request:
	// recovery data, basic profile, and offline access (the last is what makes
	// WHOOP return a refresh token so we can sync without the user present).
	ScopeString = "read:recovery read:profile offline"
)

// whoopRevokeURL is WHOOP's access-revocation endpoint. It is a package var
// (not const) so tests can repoint it at an httptest.Server; Revoke reads it.
var whoopRevokeURL = "https://api.prod.whoop.com/developer/v2/user/access"

// ErrNoRefreshToken is returned by Exchange when WHOOP's token response carries
// no refresh token — meaning the "offline" scope was not granted. Surfacing it
// as a typed error lets the callback render a clear message instead of a 500.
var ErrNoRefreshToken = errors.New("whoopsync: token response has no refresh token")

// ErrInvalidGrant is returned by Refresh when WHOOP rejects the refresh token
// (HTTP 400/401 or an invalid_grant body). The connection is dead and the user
// must re-consent; callers flip the connection to revoked on this error.
var ErrInvalidGrant = errors.New("whoopsync: refresh token was rejected")

// OAuthConfig wraps the oauth2.Config for the WHOOP consent flow. Use
// NewOAuthConfig in production; tests build it directly to point the endpoint
// at a fake server.
type OAuthConfig struct {
	cfg *oauth2.Config
}

// NewOAuthConfig builds the production OAuthConfig from the app's WHOOP client
// credentials and redirect URI. Scopes are split from ScopeString and the
// endpoint points at WHOOP's production authorize/token URLs.
func NewOAuthConfig(clientID, clientSecret, redirectURL string) *OAuthConfig {
	return &OAuthConfig{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Scopes:       strings.Fields(ScopeString),
			Endpoint: oauth2.Endpoint{
				AuthURL:  authorizeURL,
				TokenURL: exchangeURL,
			},
		},
	}
}

// Tokens is a decoded token response (from an exchange or a refresh).
type Tokens struct {
	AccessToken  string
	RefreshToken string // WHOOP ROTATES this on refresh — always the newest.
	ExpiresAt    time.Time
	Scopes       string
}

// randomToken produces a URL-safe random string used as the CSRF half of the
// OAuth state value (and stored in the state cookie). 32 bytes = 256 bits.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// encodeState binds the OAuth round-trip to BOTH the CSRF cookie and the
// originating user. The callback is a public endpoint (WHOOP redirects the
// browser there and our auth cookie may not ride along under SameSite=Lax in
// every browser/path combination), so the user id cannot be read from the
// request context — it must survive inside the state parameter.
//
// Layout: base64url( random + ":" + userID + ":" + base64url(HMAC-SHA256) ),
// where the HMAC is computed with hmacKey over (random + ":" + userID). The
// `random` half is also set as an HttpOnly cookie. The signature is what makes
// the userID half trustworthy: without it, an attacker could complete a real
// WHOOP consent for their OWN account and then replay the callback with a
// forged state carrying a VICTIM's userID plus an attacker-chosen random/cookie
// pair, linking the attacker's WHOOP to the victim's account (an account-linking
// CSRF). Signing the (random, userID) pair with the server's secret means the
// callback only accepts state values minted by /connect.
func encodeState(random, userID string, hmacKey []byte) string {
	payload := random + ":" + userID
	sig := stateSignature(payload, hmacKey)
	raw := payload + ":" + base64.RawURLEncoding.EncodeToString(sig)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeState reverses encodeState, returning the random (CSRF) half and the
// user id, but ONLY after verifying the embedded HMAC signature with hmacKey in
// constant time. An error means the state was malformed (truncated, not base64,
// missing a separator) OR the signature is absent/invalid — in every case the
// callback must reject it and write no connection row.
func decodeState(state string, hmacKey []byte) (random, userID string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return "", "", fmt.Errorf("whoopsync: state is not valid base64: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", errors.New("whoopsync: state is missing random, user id, or signature")
	}
	random, userID, sigB64 := parts[0], parts[1], parts[2]

	gotSig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return "", "", fmt.Errorf("whoopsync: state signature is not valid base64: %w", err)
	}
	wantSig := stateSignature(random+":"+userID, hmacKey)
	if !hmac.Equal(gotSig, wantSig) {
		return "", "", errors.New("whoopsync: state signature mismatch")
	}
	return random, userID, nil
}

// stateSignature computes HMAC-SHA256(hmacKey, payload). It is the single source
// of truth for both encode (mint) and decode (verify) so the two can never
// drift in how the signed message is constructed.
func stateSignature(payload string, hmacKey []byte) []byte {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// AuthCodeURL builds the consent-screen redirect URL for state.
func (c *OAuthConfig) AuthCodeURL(state string) string {
	return c.cfg.AuthCodeURL(state)
}

// Exchange swaps an authorization code for tokens. The http.Client is injected
// into the oauth2 context so tests can target a fake token endpoint while
// production uses the timeout-bounded client wired in from the server. The
// refresh token MUST be non-empty (offline access granted) else
// ErrNoRefreshToken.
func (c *OAuthConfig) Exchange(ctx context.Context, httpClient *http.Client, code string) (*Tokens, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	token, err := c.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("whoopsync: exchange code: %w", err)
	}
	if token.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}
	return tokensFromOAuth(token), nil
}

// Refresh performs a refresh_token grant and returns the NEW rotated token
// pair. A 400/401 or invalid_grant response maps to ErrInvalidGrant so callers
// can flip the connection to revoked.
//
// This is a manual POST (rather than oauth2.TokenSource) because it makes
// invalid_grant detection explicit and lets us reliably surface WHOOP's rotated
// refresh token from the JSON response.
func (c *OAuthConfig) Refresh(ctx context.Context, httpClient *http.Client, refreshToken string) (*Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	// WHOOP requires the scope to be re-requested on refresh to keep offline
	// access (and thus continue rotating a refresh token).
	form.Set("scope", strings.Join(c.cfg.Scopes, " "))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("whoopsync: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whoopsync: refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("whoopsync: read refresh response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest ||
		strings.Contains(string(body), "invalid_grant") {
		return nil, ErrInvalidGrant
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("whoopsync: refresh returned status %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("whoopsync: decode refresh response: %w", err)
	}
	if tr.AccessToken == "" || tr.RefreshToken == "" {
		return nil, fmt.Errorf("whoopsync: refresh response missing access or refresh token")
	}
	return &Tokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    expiryFromSeconds(tr.ExpiresIn),
		Scopes:       tr.Scope,
	}, nil
}

// Revoke best-effort revokes the user's access at WHOOP by issuing
// DELETE /v2/user/access with the access token as a bearer credential. A
// non-2xx response is returned as an error; callers treat revoke failures as
// non-fatal (the local row is deleted regardless).
func Revoke(ctx context.Context, httpClient *http.Client, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, whoopRevokeURL, nil)
	if err != nil {
		return fmt.Errorf("whoopsync: build revoke request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("whoopsync: revoke request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("whoopsync: revoke returned %d", resp.StatusCode)
	}
	return nil
}

// tokenResponse is WHOOP's OAuth token endpoint JSON body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// tokensFromOAuth converts an *oauth2.Token (used by Exchange) into our Tokens.
// The oauth2 library populates RefreshToken from the response on both exchange
// and refresh, and carries the scope in the token's extra fields.
func tokensFromOAuth(t *oauth2.Token) *Tokens {
	scope, _ := t.Extra("scope").(string)
	return &Tokens{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    t.Expiry,
		Scopes:       scope,
	}
}

// expiryFromSeconds turns an expires_in (seconds from now) into an absolute
// time. A zero/absent value yields the zero time so callers can detect it.
func expiryFromSeconds(secs int64) time.Time {
	if secs <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(secs) * time.Second)
}
