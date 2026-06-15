package calendarsync

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// CalendarEventsScope is the single Google OAuth scope this feature requests:
// read/write access to the user's calendar events (not their whole calendar
// list). This is the minimum needed to create/update planned-workout events.
const CalendarEventsScope = "https://www.googleapis.com/auth/calendar.events"

// googleRevokeURL is Google's OAuth token revocation endpoint. POSTing a
// refresh (or access) token here revokes the grant. It is overridable via the
// handler so tests can point it at an httptest.Server instead of Google.
const googleRevokeURL = "https://oauth2.googleapis.com/revoke"

// defaultCalendarID is the calendar we write planned-workout events to. "primary"
// is Google's well-known alias for the user's main calendar, so we never need
// to look up a concrete id — robust and avoids an extra API round-trip.
const defaultCalendarID = "primary"

// NewCalendarConfig builds the OAuth 2.0 client config for the incremental
// calendar consent flow. It mirrors the login config (internal/auth) but
// requests the calendar.events scope and a redirect URI distinct from login's
// (Google matches redirect_uri exactly).
func NewCalendarConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{CalendarEventsScope},
		Endpoint:     google.Endpoint,
	}
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
// originating user. The callback is a public endpoint (Google redirects the
// browser there and our auth cookie may not ride along under SameSite=Lax in
// every browser/path combination), so the user id cannot be read from the
// request context — it must survive inside the state parameter.
//
// Layout: base64url( random + ":" + userID + ":" + base64url(HMAC-SHA256) ),
// where the HMAC is computed with hmacKey over (random + ":" + userID). The
// `random` half is also set as an HttpOnly cookie. The signature is what makes
// the userID half trustworthy: without it, an attacker could complete a real
// Google consent for their OWN account and then replay the callback with a
// forged state carrying a VICTIM's userID plus an attacker-chosen random/cookie
// pair, linking the attacker's calendar to the victim's account (an
// account-linking CSRF). Signing the (random, userID) pair with the server's
// secret means the callback only accepts state values minted by /connect.
//
// userIDs in this system have no ":" (they are id.New() hex strings), but
// decodeState splits on the FIRST two ":" so an id containing one still
// round-trips cleanly.
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
		return "", "", fmt.Errorf("calendarsync: state is not valid base64: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", errors.New("calendarsync: state is missing random, user id, or signature")
	}
	random, userID, sigB64 := parts[0], parts[1], parts[2]

	gotSig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return "", "", fmt.Errorf("calendarsync: state signature is not valid base64: %w", err)
	}
	wantSig := stateSignature(random+":"+userID, hmacKey)
	if !hmac.Equal(gotSig, wantSig) {
		return "", "", errors.New("calendarsync: state signature mismatch")
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

// authCodeURL builds the consent-screen redirect URL for the calendar flow.
// The combination of access_type=offline + prompt=consent is what forces
// Google to return a refresh token, even on a re-consent where the user has
// already granted the scope (otherwise RefreshToken comes back empty).
// include_granted_scopes=true performs incremental authorization so the user
// keeps the login scopes they already granted.
func authCodeURL(cfg *oauth2.Config, state string) string {
	return cfg.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce, // prompt=consent
		oauth2.SetAuthURLParam("include_granted_scopes", "true"),
	)
}

// exchangeCode swaps an authorization code for a token using the provided
// http.Client (injected so tests can target a fake token endpoint via the
// config's Endpoint.TokenURL). It returns the refresh token, which MUST be
// non-empty for a usable connection — an empty refresh token means Google did
// not grant offline access (usually a missing prompt=consent).
func exchangeCode(ctx context.Context, cfg *oauth2.Config, httpClient *http.Client, code string) (refreshToken string, err error) {
	// oauth2.Exchange reads its HTTP client from the context. Setting it here
	// lets tests point at an httptest.Server while production uses the
	// timeout-bounded client wired in from the server.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("calendarsync: exchange code: %w", err)
	}
	if token.RefreshToken == "" {
		return "", ErrNoRefreshToken
	}
	return token.RefreshToken, nil
}

// ErrNoRefreshToken is returned by exchangeCode when Google's token response
// carries no refresh token. The connect flow forces prompt=consent +
// access_type=offline precisely to avoid this; surfacing it as a typed error
// lets the callback render a clear, actionable message instead of a 500.
var ErrNoRefreshToken = errors.New("calendarsync: token response has no refresh token")

// revokeToken best-effort revokes a token at Google's revocation endpoint. The
// revoke URL is parameterized so tests can substitute an httptest.Server. A
// non-2xx response is returned as an error; callers treat revoke failures as
// non-fatal (the local row is deleted regardless).
func revokeToken(ctx context.Context, httpClient *http.Client, revokeURL, token string) error {
	form := url.Values{}
	form.Set("token", token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("calendarsync: build revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calendarsync: revoke request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("calendarsync: revoke returned %d", resp.StatusCode)
	}
	return nil
}
