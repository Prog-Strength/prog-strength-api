package calendarsync

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/calendarconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/originmatch"
)

// Cookie names for the calendar OAuth flow. Deliberately distinct from the
// login flow's (oauth_state / oauth_return_to) so the two concurrent flows
// can't clobber each other's cookies if a user happens to have both in flight.
const (
	stateCookieName    = "calendar_oauth_state"
	returnToCookieName = "calendar_oauth_return_to"
)

// Handler exposes the incremental Google Calendar OAuth flow and the
// connection-status endpoints. It does NOT write calendar events — that lands
// in a later task. The handler is mounted in two pieces (see Mount /
// MountAuthed) because /callback is public (Google calls it) while /connect
// and /me/calendar/connection require the logged-in user.
type Handler struct {
	oauthConfig            *oauth2.Config
	conns                  calendarconn.Repository
	cipher                 *Cipher
	httpClient             *http.Client
	returnToAllowedOrigins []string
	// stateHMACKey signs the OAuth state's (random, userID) pair so the public
	// callback can trust the userID it recovers. It is the server's JWT signing
	// key — reused here purely as an HMAC secret, never as a JWT.
	stateHMACKey []byte
	// revokeURL is Google's revocation endpoint, overridable in tests.
	revokeURL string
}

// NewHandler constructs a Handler. oauthConfig must already carry the calendar
// scope + redirect URL (build it with NewCalendarConfig). httpClient bounds the
// token-exchange and revoke calls; pass a client with a timeout. cipher
// encrypts refresh tokens at rest. stateHMACKey (the server's JWT signing key)
// signs the OAuth state so the public callback can't be tricked into linking a
// calendar to the wrong user.
func NewHandler(oauthConfig *oauth2.Config, conns calendarconn.Repository, cipher *Cipher, httpClient *http.Client, returnToAllowedOrigins []string, stateHMACKey []byte) *Handler {
	return &Handler{
		oauthConfig:            oauthConfig,
		conns:                  conns,
		cipher:                 cipher,
		httpClient:             httpClient,
		returnToAllowedOrigins: returnToAllowedOrigins,
		stateHMACKey:           stateHMACKey,
		revokeURL:              googleRevokeURL,
	}
}

// MountPublic registers the routes that Google itself calls and that don't
// carry our auth cookie reliably: only /auth/google/calendar/callback. It is
// public because the user id is carried through the signed-ish state value
// rather than the request context. Mount this OUTSIDE the JWT-gated group.
func (h *Handler) MountPublic(r chi.Router) {
	r.Get("/auth/google/calendar/callback", h.callback)
}

// MountAuthed registers the routes that require the logged-in user. Mount this
// INSIDE the JWT-gated group (auth.RequireUser). /connect reads the user id
// from context and encodes it into the OAuth state so the public callback can
// recover it.
func (h *Handler) MountAuthed(r chi.Router) {
	r.Get("/auth/google/calendar/connect", h.connect)
	r.Get("/me/calendar/connection", h.getConnection)
	r.Delete("/me/calendar/connection", h.deleteConnection)
}

// connect (authed) redirects the user to Google's consent screen for the
// calendar scope with offline access. It reads the caller's user id from
// context and encodes it into the OAuth state alongside a fresh CSRF random;
// the random is also set as an HttpOnly cookie. The public callback recovers
// the user id from the state and verifies the random against the cookie.
func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	random, err := randomToken()
	if err != nil {
		httpresp.ServerError(w, r.Context(), "generate calendar oauth state", err)
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
	http.Redirect(w, r, authCodeURL(h.oauthConfig, state), http.StatusTemporaryRedirect)
}

// callback (public) completes the flow: validate state against the CSRF
// cookie, recover the user id from the state, exchange the code for a refresh
// token, encrypt it, and upsert the connection. On success it redirects to the
// whitelisted return_to (if any) or returns JSON.
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
	// the HttpOnly cookie (CSRF, defense in depth).
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

	refreshToken, err := exchangeCode(r.Context(), h.oauthConfig, h.httpClient, code)
	if err != nil {
		if errors.Is(err, ErrNoRefreshToken) {
			// Google didn't grant offline access — almost always means the
			// consent screen wasn't forced (prompt=consent). Surface a clear,
			// non-500 error and write NO row.
			h.failOrRedirect(w, r, returnTo, http.StatusBadRequest,
				"google did not return a refresh token; reconnect and approve offline access", "no_refresh_token")
			return
		}
		httpresp.ServerError(w, r.Context(), "exchange calendar oauth code", err)
		return
	}

	enc, nonce, err := h.cipher.Encrypt([]byte(refreshToken))
	if err != nil {
		httpresp.ServerError(w, r.Context(), "encrypt refresh token", err)
		return
	}

	if err := h.conns.Upsert(r.Context(), userID, enc, nonce, defaultCalendarID, CalendarEventsScope, time.Now()); err != nil {
		httpresp.ServerError(w, r.Context(), "upsert calendar connection", err)
		return
	}

	if returnTo != "" {
		params := url.Values{}
		params.Set("calendar", "connected")
		http.Redirect(w, r, returnTo+"#"+params.Encode(), http.StatusTemporaryRedirect)
		return
	}
	httpresp.OK(w, "calendar connected", connectionResponse{
		Status:           string(calendarconn.StatusConnected),
		GoogleCalendarID: defaultCalendarID,
		Scopes:           CalendarEventsScope,
	})
}

// connectionResponse is the GET /me/calendar/connection payload. Fields beyond
// Status are omitempty so the `absent` case returns just {"status":"absent"}.
type connectionResponse struct {
	Status           string     `json:"status"`
	GoogleCalendarID string     `json:"google_calendar_id,omitempty"`
	Scopes           string     `json:"scopes,omitempty"`
	ConnectedAt      *time.Time `json:"connected_at,omitempty"`
}

// statusAbsent is the synthetic status returned when no connection row exists.
const statusAbsent = "absent"

// getConnection (authed) reports the caller's calendar connection status:
// connected / revoked (from the stored row) or absent (no row).
func (h *Handler) getConnection(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	conn, err := h.conns.Get(r.Context(), userID)
	if errors.Is(err, calendarconn.ErrNotFound) {
		httpresp.OK(w, "calendar connection status", connectionResponse{Status: statusAbsent})
		return
	}
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get calendar connection", err)
		return
	}

	connectedAt := conn.ConnectedAt
	httpresp.OK(w, "calendar connection status", connectionResponse{
		Status:           string(conn.Status),
		GoogleCalendarID: conn.GoogleCalendarID,
		Scopes:           conn.Scopes,
		ConnectedAt:      &connectedAt,
	})
}

// deleteConnection (authed) disconnects the user's calendar: best-effort revoke
// at Google (decrypting the stored token first), then delete the local row. A
// revoke failure is logged, not fatal — the local row is removed regardless so
// the user is fully disconnected from our side. Returns 404 when absent.
func (h *Handler) deleteConnection(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok || userID == "" {
		httpresp.Error(w, http.StatusUnauthorized, "missing authenticated user")
		return
	}

	enc, nonce, err := h.conns.GetRefreshToken(r.Context(), userID)
	if errors.Is(err, calendarconn.ErrNotFound) {
		httpresp.Error(w, http.StatusNotFound, "no calendar connection")
		return
	}
	if err != nil {
		httpresp.ServerError(w, r.Context(), "get calendar refresh token", err)
		return
	}

	// Best-effort revoke. Decrypt failures and Google failures are both logged
	// and swallowed so the local delete still proceeds.
	if token, decErr := h.cipher.Decrypt(enc, nonce); decErr != nil {
		log.Printf("calendar disconnect: decrypt token for %s failed: %v", userID, decErr)
	} else if revErr := revokeToken(r.Context(), h.httpClient, h.revokeURL, string(token)); revErr != nil {
		log.Printf("calendar disconnect: revoke at google for %s failed: %v", userID, revErr)
	}

	if err := h.conns.Delete(r.Context(), userID); err != nil {
		if errors.Is(err, calendarconn.ErrNotFound) {
			httpresp.Error(w, http.StatusNotFound, "no calendar connection")
			return
		}
		httpresp.ServerError(w, r.Context(), "delete calendar connection", err)
		return
	}
	httpresp.OK(w, "calendar disconnected", nil)
}

// failOrRedirect renders a callback failure either as a redirect back to the
// frontend (with #error=<code>) when a whitelisted return_to is in play, or as
// a JSON error envelope otherwise. Mirrors the login flow's split behavior.
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
// Same logic as internal/auth; duplicated to keep this package import-clean.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

// isAllowedReturnTo reports whether a return_to URL's origin (scheme + host) is
// in the configured whitelist. Identical semantics to the login flow's
// open-redirect guard, including single-"*" wildcard entries. See
// internal/originmatch.
func (h *Handler) isAllowedReturnTo(returnTo string) bool {
	return originmatch.AllowReturnTo(returnTo, h.returnToAllowedOrigins)
}

// readAndClearReturnTo pops + re-validates the return_to cookie. Always clears
// it (so a leftover cookie can't leak across flows); returns "" on absence or
// a whitelist miss.
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
