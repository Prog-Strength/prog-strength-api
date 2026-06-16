package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/beta"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/originmatch"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// Cookie names. authTokenCookie is set on a successful login so that browser
// clients can authenticate without manually attaching the Authorization
// header. The token is also returned in the response body for non-browser
// clients (curl, integration tests).
const (
	stateCookieName    = "oauth_state"
	returnToCookieName = "oauth_return_to"
	authCookieName     = "auth_token"
)

// Config bundles the values Handler needs to mount routes. Pulled out into
// its own type so callers don't have to keep a long parameter list in sync.
type Config struct {
	JWTSecret          []byte
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	DevAuth            bool
	// ReturnToAllowedOrigins is the whitelist of frontend origins (scheme +
	// host) that /auth/google/login may redirect back to with the JWT in
	// the URL fragment. Empty disables the return_to feature and the
	// callback responds with JSON (legacy curl/test behavior).
	ReturnToAllowedOrigins []string
}

// Handler exposes authentication endpoints. It mounts Google OAuth routes
// only when all Google* fields are present, and the dev-token route only
// when DevAuth is true.
type Handler struct {
	googleConfig           *oauth2.Config
	jwtSecret              []byte
	users                  user.Repository
	devAuth                bool
	returnToAllowedOrigins []string
	// betaChecker decides whether an email passes the closed-beta gate. It
	// is consulted per login (an infrequent, human-paced event), so a single
	// indexed lookup is free and there is no in-memory cache to invalidate.
	// An empty allowlist disables the gate (the checker returns true for
	// everyone).
	betaChecker beta.Checker
}

// NewHandler constructs a Handler. users is required (find-or-create on
// login); betaChecker gates which emails receive a JWT after OAuth.
func NewHandler(cfg Config, users user.Repository, betaChecker beta.Checker) *Handler {
	var googleCfg *oauth2.Config
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" && cfg.GoogleRedirectURL != "" {
		googleCfg = newGoogleConfig(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL)
	}
	return &Handler{
		googleConfig:           googleCfg,
		jwtSecret:              cfg.JWTSecret,
		users:                  users,
		devAuth:                cfg.DevAuth,
		returnToAllowedOrigins: cfg.ReturnToAllowedOrigins,
		betaChecker:            betaChecker,
	}
}

// HasGoogle reports whether Google OAuth routes will be mounted. Useful for
// startup logging so operators can tell at a glance which auth paths are live.
func (h *Handler) HasGoogle() bool { return h.googleConfig != nil }

// Mount registers auth routes. /auth/google/* is only mounted when Google
// OAuth is configured; /auth/dev/token is only mounted when DevAuth is true.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		if h.googleConfig != nil {
			r.Get("/google/login", h.googleLogin)
			r.Get("/google/callback", h.googleCallback)
		}
		if h.devAuth {
			r.Post("/dev/token", h.devToken)
		}
	})
}

// googleLogin redirects the user to Google's consent screen with a CSRF
// state parameter that we also set as a short-lived cookie. The callback
// compares the two; mismatched state = potential CSRF, reject.
//
// Optional ?return_to=<url> query parameter tells the callback where to
// bounce the user back to after a successful login. The URL must be in
// the configured whitelist (open-redirect protection). When set, we
// stash it in a separate short-lived cookie alongside the state cookie;
// the callback reads it back to build the redirect target. When unset
// (or the cookie is lost), the callback falls back to the legacy JSON
// response shape so curl-based flows still work.
func (h *Handler) googleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		httpresp.ServerError(w, r.Context(), "generate oauth state", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
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

	authURL := h.googleConfig.AuthCodeURL(state, oauth2.AccessTypeOnline)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// googleCallback receives Google's redirect with code+state, validates the
// state against the cookie, exchanges the code for user info, finds-or-creates
// the user, and issues a JWT.
func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(stateCookieName)
	if err != nil || cookie.Value == "" || cookie.Value != state {
		httpresp.Error(w, http.StatusBadRequest, "invalid oauth state")
		return
	}
	// Clear the state cookie regardless of outcome.
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Path: "/", MaxAge: -1})

	code := r.URL.Query().Get("code")
	if code == "" {
		httpresp.Error(w, http.StatusBadRequest, "missing oauth code")
		return
	}

	gu, err := fetchGoogleUser(r.Context(), h.googleConfig, code)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "fetch google user", err)
		return
	}

	u, err := h.findOrCreateUser(r.Context(), gu.Email, gu.Name, gu.Picture)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "find or create user", err)
		return
	}

	// Always read+clear the return_to cookie so it doesn't linger if we
	// short-circuit below — even on the beta-denied path we want to use
	// the same redirect target the frontend asked for, so it can show
	// the contact-the-admins screen.
	returnTo := h.readAndClearReturnTo(w, r)

	// Beta gate: the allowlist lives in the beta_allowed_emails table
	// (consulted here via betaChecker). Only allowed emails get a JWT.
	// Anyone else completes OAuth (their user row already exists from
	// findOrCreateUser above — visibility into sign-up attempts) but
	// bounces back to the frontend with error=beta_required so the UI
	// can show a "request access" screen. Empty allowlist = open access.
	//
	// Fail closed on a checker error: a DB hiccup must not mint a token for
	// a non-allowed email, so a 500 here is preferable to leaking access.
	allowed, err := h.betaChecker.IsAllowed(r.Context(), u.Email)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "beta allowlist check", err)
		return
	}
	if !allowed {
		if returnTo != "" {
			h.redirectBetaRequired(w, r, returnTo, u.Email)
			return
		}
		// No return_to (curl-style flow). Surface the rejection as a
		// 403 with the standard error envelope so non-browser clients
		// still get a clear signal.
		httpresp.Error(w, http.StatusForbidden, "beta access required for this email")
		return
	}

	// If the login set a return_to cookie, redirect there with the JWT
	// in the URL fragment. Fragments aren't sent to servers, so the
	// token doesn't leak through Referer headers or proxy logs on the
	// way to the frontend. Otherwise fall back to the JSON response
	// shape (curl, integration tests, etc.).
	if returnTo != "" {
		h.issueTokenRedirect(w, r, u, returnTo)
		return
	}
	h.issueToken(w, r, u, "logged in via google")
}

// devTokenRequest is the body of POST /auth/dev/token.
type devTokenRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// devToken mints a JWT for the given email without going through Google.
// Mounted only when DEV_AUTH=true. The same find-or-create path is used,
// so tokens issued here are indistinguishable from real OAuth tokens once
// the response is returned — which is the point: the rest of the system
// can be tested against an identical artifact.
func (h *Handler) devToken(w http.ResponseWriter, r *http.Request) {
	var req devTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" {
		httpresp.Error(w, http.StatusBadRequest, "email is required")
		return
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.Email
	}

	u, err := h.findOrCreateUser(r.Context(), req.Email, displayName, "")
	if err != nil {
		httpresp.ServerError(w, r.Context(), "find or create user", err)
		return
	}
	h.issueToken(w, r, u, "dev token issued")
}

// findOrCreateUser looks up a user by email; if absent, creates one with a
// sensible default weight unit. The user's DisplayName comes from Google
// (or the dev-token request); they can change it later via PATCH /me.
//
// avatarURL is the OAuth provider's avatar URL (Google's `picture` claim, or
// "" for the dev-token path). On create it's stored as the avatar fallback.
// On an existing user it's opportunistically refreshed when non-empty and
// changed, so accounts created before this column existed self-heal on their
// next login.
func (h *Handler) findOrCreateUser(ctx context.Context, email, displayName, avatarURL string) (*user.User, error) {
	existing, err := h.users.GetByEmail(ctx, email)
	if err == nil {
		if avatarURL != "" && (existing.OAuthAvatarURL == nil || *existing.OAuthAvatarURL != avatarURL) {
			existing.OAuthAvatarURL = &avatarURL
			// Best-effort refresh: the user's identity is already fully
			// resolved, so a transient failure writing the avatar URL must
			// not turn an otherwise-valid login into a 500. Log and continue.
			if updErr := h.users.Update(ctx, existing); updErr != nil {
				log.Printf("oauth avatar refresh for %s failed: %v", existing.ID, updErr)
			}
		}
		return existing, nil
	}
	if !errors.Is(err, user.ErrNotFound) {
		return nil, err
	}
	newUser := &user.User{
		Email:       email,
		DisplayName: displayName,
		WeightUnit:  user.WeightUnitPounds,
		// distance_unit mirrors weight_unit: new accounts default to the
		// US-centric unit (miles), matching the migration's backfill.
		DistanceUnit: user.DistanceUnitMiles,
	}
	if avatarURL != "" {
		newUser.OAuthAvatarURL = &avatarURL
	}
	if err := h.users.Create(ctx, newUser); err != nil {
		return nil, err
	}
	// Create assigns newUser.ID. New accounts have no username yet, so
	// auto-assign a handle derived from their display name (falling back to
	// the id) so every user is addressable by a stable, unique handle. The
	// uniqueness probe consults the same repo the rest of the request uses,
	// treating ErrNotFound as "free".
	//
	// Best-effort, mirroring the avatar-refresh block above: the user is
	// already persisted, so a transient failure generating or writing the
	// handle must not turn a valid login into a 500. Log and continue with a
	// NULL username (migration 029 / a future deploy reconciles it).
	if newUser.Username == nil {
		probe := func(c string) (bool, error) {
			_, e := h.users.GetByUsername(ctx, c)
			if errors.Is(e, user.ErrNotFound) {
				return false, nil
			}
			if e != nil {
				return false, e
			}
			return true, nil
		}
		if assigned, gErr := user.GenerateHandle(newUser.DisplayName, newUser.ID, probe); gErr != nil {
			log.Printf("handle generation for %s failed: %v", newUser.ID, gErr)
		} else {
			newUser.Username = &assigned
			if err := h.users.Update(ctx, newUser); err != nil {
				newUser.Username = nil
				log.Printf("handle assignment for %s failed: %v", newUser.ID, err)
			}
		}
	}
	return newUser, nil
}

// tokenResponse is the data payload returned after a successful login.
type tokenResponse struct {
	Token     string     `json:"token"`
	ExpiresIn int        `json:"expires_in"` // seconds
	User      *user.User `json:"user"`
}

// issueToken signs a JWT for u, sets it as an HttpOnly cookie, and also
// returns it in the JSON body so non-browser clients can use it.
func (h *Handler) issueToken(w http.ResponseWriter, r *http.Request, u *user.User, message string) {
	tokenStr, err := Sign(u.ID, h.jwtSecret)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "sign jwt", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    tokenStr,
		Path:     "/",
		MaxAge:   int(JWTLifetime.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	httpresp.OK(w, message, tokenResponse{
		Token:     tokenStr,
		ExpiresIn: int(JWTLifetime.Seconds()),
		User:      u,
	})
}

// isHTTPS reports whether the inbound request was made over TLS, accounting
// for an upstream reverse proxy (Caddy in our prod setup) that terminates
// TLS and forwards plain HTTP to this server. Caddy sets X-Forwarded-Proto
// by default. This is only safe because the API binds to the docker-compose
// internal network — only Caddy can reach it, so the header can be trusted.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

// randomState produces a URL-safe random string for the OAuth state parameter.
// 32 bytes = 256 bits of entropy, plenty for CSRF protection.
func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// isAllowedReturnTo reports whether a return_to URL's origin (scheme + host)
// is in the configured whitelist. Anything not on the list is rejected to
// prevent /auth/google/login from being used as an open redirect (which
// would be a phishing primitive — attacker sends a victim a login link
// that bounces to an attacker-controlled page with the JWT in the fragment).
//
// A whitelist entry may carry a single "*" wildcard, matched the same way
// the CORS origin check matches it, so a single project-scoped pattern like
// "https://prog-strength-web-*-<scope>.vercel.app" admits every Vercel
// branch-preview origin without a per-branch allowlist entry. Entries
// without "*" stay exact: custom URL schemes (e.g. the mobile app's
// "progstrength:///auth/callback") have an empty Host, so the literal
// "progstrength://" entry matches only itself — an attacker can't smuggle a
// host like "progstrength://evil.example.com". See internal/originmatch.
func (h *Handler) isAllowedReturnTo(returnTo string) bool {
	return originmatch.AllowReturnTo(returnTo, h.returnToAllowedOrigins)
}

// readAndClearReturnTo pops the return_to cookie. Returns the value if the
// cookie was present and still passes the whitelist check (defense-in-depth:
// re-validate on every access rather than trusting the cookie was set by us).
// The cookie is always cleared, even on validation failure.
func (h *Handler) readAndClearReturnTo(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(returnToCookieName)
	// Always clear regardless of outcome — leftover cookies from a
	// previous flow would otherwise leak across logins.
	http.SetCookie(w, &http.Cookie{Name: returnToCookieName, Path: "/", MaxAge: -1})
	if err != nil || cookie.Value == "" {
		return ""
	}
	if !h.isAllowedReturnTo(cookie.Value) {
		return ""
	}
	return cookie.Value
}

// redirectBetaRequired bounces the user back to the frontend's
// callback URL with `#error=beta_required&email=<encoded>`. The hash
// fragment isn't sent to servers, so this is symmetric with how the
// success path (issueTokenRedirect) ferries the JWT — same plumbing,
// different payload. The frontend reads the error and shows a
// request-access screen.
func (h *Handler) redirectBetaRequired(w http.ResponseWriter, r *http.Request, returnTo, email string) {
	params := url.Values{}
	params.Set("error", "beta_required")
	params.Set("email", email)
	http.Redirect(w, r, returnTo+"#"+params.Encode(), http.StatusTemporaryRedirect)
}

// issueTokenRedirect signs a JWT and redirects to returnTo with the token
// in the URL fragment (`#access_token=<jwt>&expires_in=<seconds>`). The
// fragment isn't sent to servers, so the token doesn't appear in Referer
// headers or server access logs on the way to the frontend.
//
// We also set the legacy auth cookie for the API's own origin so that any
// direct hits to api.* by the same browser tab keep working.
func (h *Handler) issueTokenRedirect(w http.ResponseWriter, r *http.Request, u *user.User, returnTo string) {
	tokenStr, err := Sign(u.ID, h.jwtSecret)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "sign jwt", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    tokenStr,
		Path:     "/",
		MaxAge:   int(JWTLifetime.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})

	params := url.Values{}
	params.Set("access_token", tokenStr)
	params.Set("expires_in", strconv.Itoa(int(JWTLifetime.Seconds())))
	// Fragments use the same encoding as query strings, so url.Values.Encode
	// is the right tool here. If returnTo already had its own fragment we'd
	// overwrite it — the whitelist makes that the caller's problem, not ours.
	http.Redirect(w, r, returnTo+"#"+params.Encode(), http.StatusTemporaryRedirect)
}
