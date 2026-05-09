package config

import (
	"errors"
	"os"
)

// Config holds application configuration loaded from environment variables.
type Config struct {
	// DatabaseURL is the path to the SQLite database file.
	// If empty, the application uses in-memory repositories.
	// Example: "/data/app.db" or "./data/app.db"
	DatabaseURL string

	// ServerAddr is the address the HTTP server listens on.
	// Defaults to ":8080" if not specified.
	ServerAddr string

	// JWTSigningKey is the HMAC secret used to sign and verify JWTs.
	// Required. Generate with: openssl rand -base64 32
	JWTSigningKey string

	// GoogleClientID, GoogleClientSecret, and GoogleRedirectURL configure
	// the Google OAuth 2.0 client. If any are empty, Google login routes
	// are not mounted — useful for local-only iteration with DEV_AUTH.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string

	// DevAuth, when true, mounts POST /auth/dev/token, which mints a JWT
	// for an arbitrary email without going through Google. Intended for
	// local development and testing against deployed environments that
	// don't yet have a public OAuth redirect URI. MUST be false in any
	// publicly reachable production deployment once a real auth path exists.
	DevAuth bool

	// CORSAllowedOrigin is the single browser origin permitted to make
	// credentialed cross-origin requests to the API. Empty disables CORS,
	// which is appropriate for environments with no browser frontend
	// (curl-only access still works since CORS is browser-enforced).
	// Examples: "https://progstrength.fitness" (prod), "http://localhost:5173" (Vite dev).
	CORSAllowedOrigin string
}

// Load reads configuration from environment variables.
// Returns an error when a required value is missing.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		ServerAddr:         os.Getenv("SERVER_ADDR"),
		JWTSigningKey:      os.Getenv("JWT_SIGNING_KEY"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		DevAuth:            os.Getenv("DEV_AUTH") == "true",
		CORSAllowedOrigin:  os.Getenv("CORS_ALLOWED_ORIGIN"),
	}

	if cfg.ServerAddr == "" {
		cfg.ServerAddr = ":8080"
	}

	if cfg.JWTSigningKey == "" {
		return Config{}, errors.New("JWT_SIGNING_KEY is required")
	}

	return cfg, nil
}
