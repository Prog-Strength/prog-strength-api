package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config holds application configuration loaded from environment variables.
type Config struct {
	// DatabaseURL is the path to the SQLite database file.
	// If empty, the application uses in-memory repositories.
	// Example: "/data/app.db" or "./data/app.db"
	DatabaseURL string

	// TelemetryDatabaseURL is the path to the SQLite database file
	// holding agent telemetry. Separate from DatabaseURL so observability
	// data (high-volume, disposable) doesn't share locks or backups with
	// application data (low-volume, durable). When empty AND DatabaseURL
	// is also empty, telemetry is disabled entirely (in-memory mode).
	// When empty but DatabaseURL is set, defaults to a sibling file:
	// /data/app.db → /data/telemetry.db.
	TelemetryDatabaseURL string

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

	// GoogleCalendarRedirectURL is the OAuth redirect URI for the SECOND,
	// incremental Google consent flow that grants the calendar.events scope
	// (offline access), read from GOOGLE_CALENDAR_REDIRECT_URL. It is distinct
	// from GoogleRedirectURL (the login flow) because Google matches the
	// redirect_uri exactly. Empty (the default) disables the calendar OAuth
	// connect/callback routes — the rest of the planned-workout feature still
	// works without it. Together with CalendarTokenEncKey it gates whether
	// /auth/google/calendar/* and /me/calendar/connection are mounted.
	GoogleCalendarRedirectURL string

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

	// ReturnToAllowedOrigins is the whitelist of origins (scheme + host)
	// that /auth/google/login may redirect back to via ?return_to=<url>.
	// Frontend callers pass return_to so the OAuth callback bounces them
	// to a URL they control, with the JWT in the URL fragment. Without
	// a whitelist, return_to would be an open-redirect vulnerability.
	// Empty disables the return_to feature (callback then responds with
	// JSON, the legacy behavior).
	// Example env: "http://localhost:3000,https://app.progstrength.fitness"
	ReturnToAllowedOrigins []string

	// DailyUsageCapUSD is the per-user daily external-API spend ceiling
	// in dollars, applied uniformly to all users. Read from
	// DAILY_USAGE_CAP_USD. Default 0, which internal/usage treats as
	// "capping disabled" (GET /me/usage reports uncapped). Additive and
	// safe to omit — the endpoint and migration ship before any caller.
	DailyUsageCapUSD float64

	// UsagePriceTableJSON is an OPTIONAL override of the hardcoded price
	// table in usage.DefaultPriceTable. Read from USAGE_PRICE_TABLE_JSON.
	// Default "" yields the defaults — the env var exists as an emergency
	// escape hatch (e.g. a sudden Anthropic price change you want to
	// reflect before merging a code update), not as the normal path.
	// Public rates live in source so price changes are reviewable diffs.
	UsagePriceTableJSON string

	// BetaAllowedEmails is the one-time seed source for the DB-backed beta
	// allowlist. The live gate now reads the beta_allowed_emails table (see
	// internal/beta); on first boot, if that table is empty and this is
	// non-empty, the values are seeded into it (internal/beta.SeedFromEnv,
	// added_by="seed:BETA_ALLOWED_EMAILS"). Once the table is populated this
	// var no longer affects the gate and is slated for removal. Comparison
	// (in the table) is case-insensitive; an empty table disables the gate
	// entirely — every authenticated user gets a token (pre-beta / local dev).
	BetaAllowedEmails []string

	// AdminEmails is the comma-separated operator allowlist that gates the
	// /admin/beta-emails surface (manage the beta allowlist at runtime).
	// Parsed from ADMIN_EMAILS via splitCSV; comparison is case-insensitive.
	// Empty disables the admin surface entirely (fail-closed) — every admin
	// route returns 403 until an operator is configured.
	AdminEmails []string

	// AvatarBucketName is the S3 bucket for user-uploaded avatars, read from
	// AVATAR_BUCKET_NAME. Empty (the default) means avatar storage is
	// unconfigured: GET/PATCH /me still work, but POST/DELETE /me/avatar
	// return 503. Mirrors TCX_BUCKET_NAME; additive, no new required vars.
	AvatarBucketName string

	// FatSecretClientID and FatSecretClientSecret are the OAuth2
	// client-credentials pair for the FatSecret Platform API (restaurant
	// + branded food lookup), read from FATSECRET_CLIENT_ID and
	// FATSECRET_CLIENT_SECRET. Both empty (the default) means the
	// provider is unconfigured and skipped — GET /nutrition/lookup
	// degrades per the AvatarBucketName "absent = feature degrades with
	// a clear message" pattern (503 lookup_unavailable when no provider
	// at all is configured).
	FatSecretClientID     string
	FatSecretClientSecret string

	// USDAFDCAPIKey is the USDA FoodData Central API key (generic /
	// homemade food lookup), read from USDA_FDC_API_KEY. Empty (the
	// default) means the USDA provider is unconfigured and skipped —
	// same degradation pattern as the FatSecret pair above.
	USDAFDCAPIKey string

	// CalendarTokenEncKey is the base64-encoded 32-byte AES-256-GCM key used to
	// encrypt stored Google refresh tokens, read from CALENDAR_TOKEN_ENC_KEY.
	// Empty (the default) disables Google Calendar sync entirely — the planned-
	// workout feature still works without it. Provided via the same secret
	// delivery as JWT_SIGNING_KEY/GOOGLE_CLIENT_SECRET.
	CalendarTokenEncKey string

	// LogLevel gates the structured (slog) loggers, read from LOG_LEVEL
	// ("debug", "info", "warn", "error"; case-insensitive; default
	// "info"). Currently consumed only by the nutrition lookup logger —
	// the first slog beachhead; the rest of the codebase still uses
	// log.Printf, which this does not affect. The prod compose file
	// sets debug while the lookup feature is under active development;
	// flip it to info there once things reach steady state.
	LogLevel slog.Level
}

// Load reads configuration from environment variables.
// Returns an error when a required value is missing.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:               os.Getenv("DATABASE_URL"),
		TelemetryDatabaseURL:      os.Getenv("TELEMETRY_DATABASE_URL"),
		ServerAddr:                os.Getenv("SERVER_ADDR"),
		JWTSigningKey:             os.Getenv("JWT_SIGNING_KEY"),
		GoogleClientID:            os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:        os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:         os.Getenv("GOOGLE_REDIRECT_URL"),
		GoogleCalendarRedirectURL: os.Getenv("GOOGLE_CALENDAR_REDIRECT_URL"),
		DevAuth:                   os.Getenv("DEV_AUTH") == "true",
		CORSAllowedOrigin:         os.Getenv("CORS_ALLOWED_ORIGIN"),
		ReturnToAllowedOrigins:    splitCSV(os.Getenv("RETURN_TO_ALLOWED_ORIGINS")),
		DailyUsageCapUSD:          parseFloatDefault(os.Getenv("DAILY_USAGE_CAP_USD"), 0),
		UsagePriceTableJSON:       os.Getenv("USAGE_PRICE_TABLE_JSON"),
		BetaAllowedEmails:         splitCSV(os.Getenv("BETA_ALLOWED_EMAILS")),
		AdminEmails:               splitCSV(os.Getenv("ADMIN_EMAILS")),
		AvatarBucketName:          os.Getenv("AVATAR_BUCKET_NAME"),
		FatSecretClientID:         os.Getenv("FATSECRET_CLIENT_ID"),
		FatSecretClientSecret:     os.Getenv("FATSECRET_CLIENT_SECRET"),
		USDAFDCAPIKey:             os.Getenv("USDA_FDC_API_KEY"),
		CalendarTokenEncKey:       os.Getenv("CALENDAR_TOKEN_ENC_KEY"),
	}

	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	// Default telemetry path next to app.db when the user set the app
	// path but not the telemetry one. Keeps the common case zero-config
	// while still allowing explicit override.
	if cfg.TelemetryDatabaseURL == "" && cfg.DatabaseURL != "" {
		cfg.TelemetryDatabaseURL = deriveTelemetryPath(cfg.DatabaseURL)
	}

	if cfg.ServerAddr == "" {
		cfg.ServerAddr = ":8080"
	}

	if cfg.JWTSigningKey == "" {
		return Config{}, errors.New("JWT_SIGNING_KEY is required")
	}

	return cfg, nil
}

// deriveTelemetryPath returns the conventional telemetry.db path
// sitting alongside the given app.db path. Used when TELEMETRY_DATABASE_URL
// is unset but DATABASE_URL is — saves operators from setting two env
// vars when they want the obvious default.
func deriveTelemetryPath(appDB string) string {
	// "/data/app.db" → "/data/telemetry.db"
	// "./app.db"    → "./telemetry.db"
	// "foo/bar.db"  → "foo/telemetry.db"
	if i := strings.LastIndex(appDB, "/"); i >= 0 {
		return appDB[:i+1] + "telemetry.db"
	}
	return "telemetry.db"
}

// parseFloatDefault parses a float env value, falling back to def on an
// empty or unparseable string. Used for DAILY_USAGE_CAP_USD so a missing
// or malformed value disables capping rather than failing startup — the
// feature is additive and must not block boot.
func parseFloatDefault(s string, def float64) float64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

// splitCSV trims and drops empty entries from a comma-separated env var.
// Returns nil for empty input so callers can do a single nil-check
// instead of len()==0.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// parseLogLevel maps LOG_LEVEL to a slog.Level. Empty defaults to
// info; an unrecognized value is a startup error (fail fast beats
// silently logging at the wrong verbosity).
func parseLogLevel(raw string) (slog.Level, error) {
	if raw == "" {
		return slog.LevelInfo, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("LOG_LEVEL: unrecognized level %q (use debug, info, warn, or error)", raw)
	}
	return level, nil
}
