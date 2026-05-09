package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"golang.org/x/oauth2"
)

// Cookie names. authTokenCookie is set on a successful login so that browser
// clients can authenticate without manually attaching the Authorization
// header. The token is also returned in the response body for non-browser
// clients (curl, integration tests).
const (
	stateCookieName = "oauth_state"
	authCookieName  = "auth_token"
)

// Config bundles the values Handler needs to mount routes. Pulled out into
// its own type so callers don't have to keep a long parameter list in sync.
type Config struct {
	JWTSecret          []byte
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	DevAuth            bool
}

// Handler exposes authentication endpoints. It mounts Google OAuth routes
// only when all Google* fields are present, and the dev-token route only
// when DevAuth is true.
type Handler struct {
	googleConfig *oauth2.Config
	jwtSecret    []byte
	users        user.Repository
	devAuth      bool
}

// NewHandler constructs a Handler. users is required (find-or-create on login).
func NewHandler(cfg Config, users user.Repository) *Handler {
	var googleCfg *oauth2.Config
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" && cfg.GoogleRedirectURL != "" {
		googleCfg = newGoogleConfig(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL)
	}
	return &Handler{
		googleConfig: googleCfg,
		jwtSecret:    cfg.JWTSecret,
		users:        users,
		devAuth:      cfg.DevAuth,
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
	url := h.googleConfig.AuthCodeURL(state, oauth2.AccessTypeOnline)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
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

	u, err := h.findOrCreateUser(r.Context(), gu.Email, gu.Name)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "find or create user", err)
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

	u, err := h.findOrCreateUser(r.Context(), req.Email, displayName)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "find or create user", err)
		return
	}
	h.issueToken(w, r, u, "dev token issued")
}

// findOrCreateUser looks up a user by email; if absent, creates one with a
// sensible default weight unit. The user's DisplayName comes from Google
// (or the dev-token request); they can change it later via a user-update
// endpoint that doesn't exist yet.
func (h *Handler) findOrCreateUser(ctx context.Context, email, displayName string) (*user.User, error) {
	existing, err := h.users.GetByEmail(ctx, email)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, user.ErrNotFound) {
		return nil, err
	}
	newUser := &user.User{
		Email:       email,
		DisplayName: displayName,
		WeightUnit:  user.WeightUnitPounds,
	}
	if err := h.users.Create(ctx, newUser); err != nil {
		return nil, err
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
