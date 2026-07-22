package whoopsync

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/originmatch"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/tokencrypt"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
)

// Cookie names for the WHOOP OAuth flow. Deliberately distinct from the login
// flow's and the calendar flow's cookies so the concurrent flows can't clobber
// each other's state if a user happens to have more than one in flight.
const (
	stateCookieName    = "whoop_oauth_state"
	returnToCookieName = "whoop_oauth_return_to"
)

// statusAbsent is the synthetic status returned when no connection row exists.
const statusAbsent = "absent"

// Handler exposes the WHOOP OAuth connect/callback flow and the
// connection-status endpoints. It is mounted in two pieces (see MountPublic /
// MountAuthed) because /callback is public (WHOOP redirects the browser there
// and our auth cookie may not ride along under SameSite=Lax) while /connect and
// /me/whoop/connection require the logged-in user.
type Handler struct {
	oauth                  *OAuthConfig
	client                 *Client
	conns                  whoopconn.Repository
	svc                    *Service // for Backfill on first connect
	cipher                 *tokencrypt.Cipher
	httpClient             *http.Client
	returnToAllowedOrigins []string
	// stateHMACKey signs the OAuth state's (random, userID) pair so the public
	// callback can trust the userID it recovers. It is the server's JWT signing
	// key — reused here purely as an HMAC secret, never as a JWT.
	stateHMACKey []byte
	now          func() time.Time
}

// NewHandler constructs a Handler. oauth carries the WHOOP client credentials +
// redirect URL and scopes (build it with NewOAuthConfig). client fetches the
// WHOOP profile after exchange. httpClient bounds the token-exchange and revoke
// calls; pass a client with a timeout. cipher encrypts tokens at rest.
// stateHMACKey (the server's JWT signing key) signs the OAuth state so the
// public callback can't be tricked into linking a WHOOP account to the wrong
// user. now defaults to time.Now when nil.
func NewHandler(oauth *OAuthConfig, client *Client, conns whoopconn.Repository, svc *Service, cipher *tokencrypt.Cipher, httpClient *http.Client, returnToAllowedOrigins []string, stateHMACKey []byte, now func() time.Time) *Handler {
	if now == nil {
		now = time.Now
	}
	return &Handler{
		oauth:                  oauth,
		client:                 client,
		conns:                  conns,
		svc:                    svc,
		cipher:                 cipher,
		httpClient:             httpClient,
		returnToAllowedOrigins: returnToAllowedOrigins,
		stateHMACKey:           stateHMACKey,
		now:                    now,
	}
}

// MountPublic registers the route that WHOOP itself calls and that doesn't carry
// our auth cookie reliably: only /auth/whoop/callback. It is public because the
// user id is carried through the HMAC-signed state value rather than the request
// context. Mount this OUTSIDE the JWT-gated group.
func (h *Handler) MountPublic(r chi.Router) {
	r.Get("/auth/whoop/callback", h.callback)
}

// MountAuthed registers the routes that require the logged-in user. Mount this
// INSIDE the JWT-gated group (auth.RequireUser). /connect reads the user id from
// context and encodes it into the OAuth state so the public callback can recover
// it.
func (h *Handler) MountAuthed(r chi.Router) {
	r.Get("/auth/whoop/connect", h.connect)
	r.Get("/me/whoop/connection", h.getConnection)
	r.Delete("/me/whoop/connection", h.deleteConnection)
}

// connect (authed) redirects the user to WHOOP's consent screen. It reads the
// caller's user id from context and encodes it into the OAuth state alongside a
// fresh CSRF random; the random is also set as an HttpOnly cookie. The public
// callback recovers the user id from the state and verifies the random against
// the cookie.
func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	random, err := randomToken()
	if err != nil {
		httpresp.ServerError(w, r.Context(), "generate whoop oauth state", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    random,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})

	if returnTo := r.URL.Query().Get("return_to"); returnTo != "" {
		if !h.isAllowedReturnTo(returnTo) {
			httpresp.Error(w, http.StatusBadRequest, "return_to origin is not allowed")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     returnToCookieName,
			Value:    returnTo,
			Path:     "/",
			MaxAge:   600,
			HttpOnly: true,
			Secure:   isHTTPS(r),
			SameSite: http.SameSiteLaxMode,
		})
	}

	state := encodeState(random, userID, h.stateHMACKey)
	http.Redirect(w, r, h.oauth.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

// callback (public) completes the flow: validate state against the CSRF cookie,
// recover the user id from the state, exchange the code for tokens, fetch the
// WHOOP profile (for whoop_user_id), encrypt the tokens, and upsert the
// connection. A best-effort backfill is kicked off inline; its failure does not
// fail the callback. On success it redirects to the whitelisted return_to (if
// any) or returns JSON.
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(stateCookieName)
	// Clear the state cookie regardless of outcome.
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Path: "/", MaxAge: -1})
	if err != nil || cookie.Value == "" || state == "" {
		httpresp.Error(w, http.StatusBadRequest, "invalid oauth state")
		return
	}
	// Verify BOTH the HMAC signature (proves the state was minted by /connect,
	// so the recovered userID is trustworthy) AND that the random half matches
	// the HttpOnly cookie (CSRF, defense in depth). decodeState compares the
	// signature in constant time; the random==cookie check is a plain string
	// equality of two random 256-bit values.
	random, userID, err := decodeState(state, h.stateHMACKey)
	if err != nil || random != cookie.Value {
		httpresp.Error(w, http.StatusBadRequest, "invalid oauth state")
		return
	}

	returnTo := h.readAndClearReturnTo(w, r)

	code := r.URL.Query().Get("code")
	if code == "" {
		httpresp.Error(w, http.StatusBadRequest, "missing oauth code")
		return
	}

	tokens, err := h.oauth.Exchange(r.Context(), h.httpClient, code)
	if err != nil {
		if errors.Is(err, ErrNoRefreshToken) {
			// WHOOP didn't grant offline access — the "offline" scope wasn't
			// approved. Surface a clear, non-500 error and write NO row.
			h.failOrRedirect(w, r, returnTo, http.StatusBadRequest,
				"whoop did not return a refresh token; reconnect and approve offline access", "no_refresh_token")
			return
		}
		httpresp.ServerError(w, r.Context(), "exchange whoop oauth code", err)
		return
	}

	profile, err := h.client.Profile(r.Context(), tokens.AccessToken)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "fetch whoop profile", err)
		return
	}

	accessEnc, accessNonce, err := h.cipher.Encrypt([]byte(tokens.AccessToken))
	if err != nil {
		httpresp.ServerError(w, r.Context(), "encrypt whoop access token", err)
		return
	}
	refreshEnc, refreshNonce, err := h.cipher.Encrypt([]byte(tokens.RefreshToken))
	if err != nil {
		httpresp.ServerError(w, r.Context(), "encrypt whoop refresh token", err)
		return
	}

	bundle := whoopconn.TokenBundle{
		AccessTokenEnc:    accessEnc,
		AccessTokenNonce:  accessNonce,
		RefreshTokenEnc:   refreshEnc,
		RefreshTokenNonce: refreshNonce,
		ExpiresAt:         tokens.ExpiresAt,
	}
	if err := h.conns.Upsert(r.Context(), userID, profile.UserID, bundle, tokens.Scopes, h.now()); err != nil {
		httpresp.ServerError(w, r.Context(), "upsert whoop connection", err)
		return
	}

	// Kick off a historical backfill inline, best-effort: a failure here must
	// NOT fail the callback — the connection is already usable, and the next
	// scheduled/webhook sync will fill in any gap.
	if err := h.svc.Backfill(r.Context(), userID); err != nil {
		log.Printf("whoop connect: backfill for %s failed (non-fatal): %v", userID, err)
	}

	if returnTo != "" {
		params := url.Values{}
		params.Set("whoop", "connected")
		http.Redirect(w, r, returnTo+"#"+params.Encode(), http.StatusTemporaryRedirect)
		return
	}
	httpresp.OK(w, "whoop connected", connectionResponse{
		Status: string(whoopconn.StatusConnected),
		Scopes: tokens.Scopes,
	})
}

// connectionResponse is the GET /me/whoop/connection payload. Fields beyond
// Status are omitempty so the `absent` case returns just {"status":"absent"}.
type connectionResponse struct {
	Status      string     `json:"status"`
	Scopes      string     `json:"scopes,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

// getConnection (authed) reports the caller's WHOOP connection status:
// connected / revoked / error (from the stored row) or absent (no row).
func (h *Handler) getConnection(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	conn, err := h.conns.Get(r.Context(), userID)
	if errors.Is(err, whoopconn.ErrNotFound) {
		httpresp.OK(w, "whoop connection status", connectionResponse{Status: statusAbsent})
		return
	}
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get whoop connection", err)
		return
	}

	connectedAt := conn.ConnectedAt
	httpresp.OK(w, "whoop connection status", connectionResponse{
		Status:      string(conn.Status),
		Scopes:      conn.Scopes,
		ConnectedAt: &connectedAt,
	})
}

// deleteConnection (authed) disconnects the user's WHOOP: best-effort revoke at
// WHOOP (decrypting the stored access token first), then mark the local row
// revoked and wipe its tokens. A revoke failure is logged, not fatal — the row
// is revoked regardless so the user is fully disconnected from our side.
// Recovery data rows are left untouched. Returns 204 on success, 404 when absent.
func (h *Handler) deleteConnection(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	// Best-effort revoke: load + decrypt the access token, then hit WHOOP.
	// Absence is fine here (Revoke below reports NotFound); decrypt and WHOOP
	// failures are logged and swallowed so the local revoke still proceeds.
	if bundle, tokErr := h.conns.GetTokens(r.Context(), userID); tokErr == nil {
		if token, decErr := h.cipher.Decrypt(bundle.AccessTokenEnc, bundle.AccessTokenNonce); decErr != nil {
			log.Printf("whoop disconnect: decrypt access token for %s failed: %v", userID, decErr)
		} else if revErr := Revoke(r.Context(), h.httpClient, string(token)); revErr != nil {
			log.Printf("whoop disconnect: revoke at whoop for %s failed: %v", userID, revErr)
		}
	}

	if err := h.conns.Revoke(r.Context(), userID, h.now()); err != nil {
		if errors.Is(err, whoopconn.ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "no whoop connection")
			return
		}
		httpresp.ServerError(w, r.Context(), "revoke whoop connection", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// failOrRedirect renders a callback failure either as a redirect back to the
// frontend (with #error=<code>) when a whitelisted return_to is in play, or as a
// JSON error envelope otherwise. Mirrors the calendar flow's split behavior.
func (h *Handler) failOrRedirect(w http.ResponseWriter, r *http.Request, returnTo string, status int, msg, code string) {
	if returnTo != "" {
		params := url.Values{}
		params.Set("error", code)
		http.Redirect(w, r, returnTo+"#"+params.Encode(), http.StatusTemporaryRedirect)
		return
	}
	httpresp.ErrorWithCode(w, status, msg, code)
}

// isHTTPS reports whether the inbound request was made over TLS, accounting for
// an upstream reverse proxy that terminates TLS (Caddy sets X-Forwarded-Proto).
// Same logic as internal/auth and calendarsync; duplicated to keep this package
// import-clean.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

// isAllowedReturnTo reports whether a return_to URL's origin (scheme + host) is
// in the configured whitelist. Identical semantics to the login/calendar flows'
// open-redirect guard, including single-"*" wildcard entries. See
// internal/originmatch.
func (h *Handler) isAllowedReturnTo(returnTo string) bool {
	return originmatch.AllowReturnTo(returnTo, h.returnToAllowedOrigins)
}

// readAndClearReturnTo pops + re-validates the return_to cookie. Always clears
// it (so a leftover cookie can't leak across flows); returns "" on absence or a
// whitelist miss.
func (h *Handler) readAndClearReturnTo(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(returnToCookieName)
	http.SetCookie(w, &http.Cookie{Name: returnToCookieName, Path: "/", MaxAge: -1})
	if err != nil || cookie.Value == "" {
		return ""
	}
	if !h.isAllowedReturnTo(cookie.Value) {
		return ""
	}
	return cookie.Value
}
