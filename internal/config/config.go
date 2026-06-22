package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config holds application configuration. Its source is the committed
// config.toml manifest (embedded, or an external CONFIG_FILE), overlaid
// with ${VAR} interpolation and explicit env overrides — but the rest of
// the codebase still consumes Config exactly as before. See Load.
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
	// (offline access). It is distinct from GoogleRedirectURL (the login
	// flow) because Google matches the redirect_uri exactly. Empty (the
	// default) disables the calendar OAuth connect/callback routes — the
	// rest of the planned-workout feature still works without it. Together
	// with CalendarTokenEncKey it gates whether /auth/google/calendar/*
	// and /me/calendar/connection are mounted.
	GoogleCalendarRedirectURL string

	// DevAuth, when true, mounts POST /auth/dev/token, which mints a JWT
	// for an arbitrary email without going through Google. Intended for
	// local development and testing against deployed environments that
	// don't yet have a public OAuth redirect URI. MUST be false in any
	// publicly reachable production deployment once a real auth path exists.
	DevAuth bool

	// CORSAllowedOrigins is the set of browser origins permitted to make
	// credentialed cross-origin requests to the API. Empty disables CORS,
	// which is appropriate for environments with no browser frontend
	// (curl-only access still works since CORS is browser-enforced).
	//
	// Each entry may contain a SINGLE "*" wildcard (go-chi/cors), which is
	// what makes Vercel preview deployments work: their hostnames carry one
	// dynamic segment per branch/commit, e.g.
	//   https://prog-strength-web-git-feat-xyz-abc123-<scope>.vercel.app
	// so a pattern like
	//   https://prog-strength-web-*-<scope>.vercel.app
	// matches every branch preview without per-branch ops. go-chi reflects
	// the concrete matched origin back in Access-Control-Allow-Origin (not
	// the literal "*"), so AllowCredentials stays valid. Scope the wildcard
	// to your project + Vercel scope — never a bare "*.vercel.app".
	CORSAllowedOrigins []string

	// ReturnToAllowedOrigins is the whitelist of origins (scheme + host)
	// that /auth/google/login may redirect back to via ?return_to=<url>.
	// Frontend callers pass return_to so the OAuth callback bounces them
	// to a URL they control, with the JWT in the URL fragment. Without
	// a whitelist, return_to would be an open-redirect vulnerability.
	// Empty disables the return_to feature (callback then responds with
	// JSON, the legacy behavior).
	//
	// Like CORSAllowedOrigins, each entry may contain a SINGLE "*" wildcard
	// (see internal/originmatch), which is what lets Vercel preview
	// deployments complete login. Scope the wildcard to your project +
	// Vercel scope — never a bare "*.vercel.app", which would reopen the
	// open-redirect hole.
	ReturnToAllowedOrigins []string

	// DailyUsageCapUSD is the per-user daily external-API spend ceiling
	// in dollars, applied uniformly to all users. Default 0, which
	// internal/usage treats as "capping disabled" (GET /me/usage reports
	// uncapped). Additive and safe to omit.
	DailyUsageCapUSD float64

	// UsagePriceTableJSON is an OPTIONAL override of the hardcoded price
	// table in usage.DefaultPriceTable. Default "" yields the defaults —
	// it exists as an emergency escape hatch (e.g. a sudden Anthropic price
	// change you want to reflect before merging a code update), not as the
	// normal path. Public rates live in source so price changes are
	// reviewable diffs.
	UsagePriceTableJSON string

	// AdminEmails is the operator allowlist that gates the
	// /admin/beta-emails surface (manage the beta allowlist at runtime).
	// Comparison is case-insensitive. Empty disables the admin surface
	// entirely (fail-closed) — every admin route returns 403 until an
	// operator is configured.
	AdminEmails []string

	// AvatarBucketName is the S3 bucket for user-uploaded avatars. Empty
	// (the default) means avatar storage is unconfigured: GET/PATCH /me
	// still work, but POST/DELETE /me/avatar return 503.
	AvatarBucketName string

	// TCXBucketName is the S3 bucket for archived TCX uploads. Empty (the
	// default) falls back to an in-memory archiver (dev only): uploaded TCX
	// bytes vanish on restart, though the DB rows survive. Previously read
	// by a stray os.Getenv in internal/server; now folded into Config.
	TCXBucketName string

	// AWSRegion is the AWS region for the S3 clients, sourced from
	// AWS_REGION (Terraform-owned). REQUIRED when either bucket is set —
	// the SDK clients resolve their region from the same env var, and a
	// configured bucket with no region fails endpoint resolution at the
	// first request. Validated at startup so the failure is loud and early.
	AWSRegion string

	// FatSecretClientID and FatSecretClientSecret are the OAuth2
	// client-credentials pair for the FatSecret Platform API (restaurant
	// + branded food lookup). Both empty (the default) means the provider
	// is unconfigured and skipped — GET /nutrition/lookup degrades to 503
	// lookup_unavailable when no provider at all is configured.
	FatSecretClientID     string
	FatSecretClientSecret string

	// USDAFDCAPIKey is the USDA FoodData Central API key (generic /
	// homemade food lookup). Empty (the default) means the USDA provider
	// is unconfigured and skipped — same degradation pattern as the
	// FatSecret pair above.
	USDAFDCAPIKey string

	// CalendarTokenEncKey is the base64-encoded 32-byte AES-256-GCM key used
	// to encrypt stored Google refresh tokens. Empty (the default) disables
	// Google Calendar sync entirely — the planned-workout feature still
	// works without it.
	CalendarTokenEncKey string

	// LogLevel gates the structured (slog) loggers ("debug", "info",
	// "warn", "error"; case-insensitive; default "info").
	LogLevel slog.Level

	// VectorMemory configures the Agent Vector Memory feature
	// (sows/agent-vector-memory.md). This SOW registers the section as the
	// foundation that feature builds on; see VectorMemoryConfig.
	VectorMemory VectorMemoryConfig

	// HRZones configures the running heart-rate-zone engine
	// (sows/running-heart-rate-zones.md): the reference-max-HR estimation
	// tunables and the five-zone percent-of-max model. See HRZonesConfig.
	HRZones HRZonesConfig
}

// HRZonesConfig groups the heart-rate-zone engine tunables. All are non-secret
// public literals (no ${VAR} interpolation, no env override): PopulationDefaultMaxHR
// seeds the cold-start reference, CalibratedRunThreshold is the run count at which
// the reference is trusted, RecencyWindowDays bounds the history considered,
// MinReferenceBpm/MaxReferenceBpm clamp the estimate to a plausible band, and
// ZoneUpperBounds/ZoneNames define the zone model (len(ZoneNames) ==
// len(ZoneUpperBounds)+1).
type HRZonesConfig struct {
	PopulationDefaultMaxHR int
	CalibratedRunThreshold int
	RecencyWindowDays      int
	MinReferenceBpm        int
	MaxReferenceBpm        int
	ZoneUpperBounds        []float64
	ZoneNames              []string
}

// VectorMemoryConfig groups the Agent Vector Memory settings. Enabled is the
// master kill-switch (false ⇒ no distillation, no retrieval, no behavior
// change). The API keys are secrets sourced from the environment; the rest
// are public tuning knobs. DistanceThreshold / DedupThreshold default to 0
// pending empirical calibration.
type VectorMemoryConfig struct {
	Enabled            bool
	OpenAIAPIKey       string
	AnthropicAPIKey    string
	DistanceThreshold  float64
	DedupThreshold     float64
	TopK               int
	SessionIdleMinutes int
	DistillModel       string
	EmbedModel         string
	EmbedDim           int
}

// fileConfig mirrors the config.toml sections. It is the decode target; Load
// maps it to the flat Config the rest of the codebase consumes.
type fileConfig struct {
	Server struct {
		Addr    string `toml:"addr"`
		DevAuth bool   `toml:"dev_auth"`
	} `toml:"server"`
	Database struct {
		URL          string `toml:"url"`
		TelemetryURL string `toml:"telemetry_url"`
	} `toml:"database"`
	Logging struct {
		Level string `toml:"level"`
	} `toml:"logging"`
	Auth struct {
		JWTSigningKey string `toml:"jwt_signing_key"`
		AdminEmails   string `toml:"admin_emails"`
		Google        struct {
			ClientID            string `toml:"client_id"`
			ClientSecret        string `toml:"client_secret"`
			LoginRedirectURL    string `toml:"login_redirect_url"`
			CalendarRedirectURL string `toml:"calendar_redirect_url"`
			CalendarTokenEncKey string `toml:"calendar_token_enc_key"`
		} `toml:"google"`
	} `toml:"auth"`
	// CORS list fields decode as any because a key may be written either as
	// a native TOML array (the config.toml literals) or as a single quoted
	// "${VAR}" / CSV string (an env-sourced or override value). go-toml/v2
	// has no array-aware custom-unmarshal hook, so toStringList normalizes
	// both shapes (interpolating + splitting) after decode.
	CORS struct {
		AllowedOrigins         any `toml:"allowed_origins"`
		ReturnToAllowedOrigins any `toml:"return_to_allowed_origins"`
	} `toml:"cors"`
	Storage struct {
		AvatarBucketName string `toml:"avatar_bucket_name"`
		TCXBucketName    string `toml:"tcx_bucket_name"`
		AWSRegion        string `toml:"aws_region"`
	} `toml:"storage"`
	Usage struct {
		DailyCapUSD float64 `toml:"daily_cap_usd"`
		PriceTable  string  `toml:"price_table"`
	} `toml:"usage"`
	NutritionLookup struct {
		FatSecretClientID     string `toml:"fatsecret_client_id"`
		FatSecretClientSecret string `toml:"fatsecret_client_secret"`
		USDAFDCAPIKey         string `toml:"usda_fdc_api_key"`
	} `toml:"nutrition_lookup"`
	VectorMemory struct {
		Enabled            bool    `toml:"enabled"`
		OpenAIAPIKey       string  `toml:"openai_api_key"`
		AnthropicAPIKey    string  `toml:"anthropic_api_key"`
		DistanceThreshold  float64 `toml:"distance_threshold"`
		DedupThreshold     float64 `toml:"dedup_threshold"`
		TopK               int     `toml:"top_k"`
		SessionIdleMinutes int     `toml:"session_idle_minutes"`
		DistillModel       string  `toml:"distill_model"`
		EmbedModel         string  `toml:"embed_model"`
		EmbedDim           int     `toml:"embed_dim"`
	} `toml:"vectormemory"`
	HRZones struct {
		PopulationDefaultMaxHR int       `toml:"population_default_max_hr"`
		CalibratedRunThreshold int       `toml:"calibrated_run_threshold"`
		RecencyWindowDays      int       `toml:"recency_window_days"`
		MinReferenceBpm        int       `toml:"min_reference_bpm"`
		MaxReferenceBpm        int       `toml:"max_reference_bpm"`
		ZoneUpperBounds        []float64 `toml:"zone_upper_bounds"`
		ZoneNames              []string  `toml:"zone_names"`
	} `toml:"hr_zones"`
}

// toStringList normalizes a decoded list value — a native TOML array, a
// single CSV string, or nil — into a flat []string, interpolating any ${VAR}
// entries and splitting on commas along the way. Returns nil for an empty
// list so callers can nil-check instead of len()==0.
func toStringList(v any) ([]string, error) {
	var raw []string
	switch val := v.(type) {
	case nil:
		return nil, nil
	case string:
		raw = []string{val}
	case []string:
		raw = val
	case []any:
		for _, e := range val {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("config: list entry %v is not a string", e)
			}
			raw = append(raw, s)
		}
	default:
		return nil, fmt.Errorf("config: unsupported list type %T", v)
	}
	var out []string
	for _, e := range raw {
		out = append(out, splitCSV(interp(e))...)
	}
	return out, nil
}

// envRef matches a value that is exactly a single ${VAR} reference.
var envRef = regexp.MustCompile(`^\$\{([A-Z0-9_]+)\}$`)

// interp resolves a bare "${VAR}" placeholder to its environment value. A
// value that is not a bare ${VAR} reference is returned unchanged. A ${VAR}
// whose env value is unset resolves to "" — treated downstream as "unset"
// (optional features degrade exactly as before; required values trip
// validation).
func interp(s string) string {
	if m := envRef.FindStringSubmatch(s); m != nil {
		return os.Getenv(m[1])
	}
	return s
}

// Load reads the manifest, interpolates ${VAR} labels, overlays explicit env
// overrides, applies defaults, validates, and produces a Config. defaultTOML
// is the embedded manifest; if CONFIG_FILE is set, that file replaces it
// wholesale. Returns an error when a required value is missing or invalid.
func Load(defaultTOML []byte) (Config, error) {
	raw := defaultTOML
	if path := os.Getenv("CONFIG_FILE"); path != "" {
		// filepath.Clean keeps gosec's G304 happy and normalizes the
		// operator-supplied path; the file is operator-controlled, not
		// user input.
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return Config{}, fmt.Errorf("config: CONFIG_FILE %q: %w", path, err)
		}
		raw = data
	}

	var fc fileConfig
	if err := toml.Unmarshal(raw, &fc); err != nil {
		return Config{}, fmt.Errorf("config: parsing manifest: %w", err)
	}

	interpolate(&fc)
	applyEnvOverrides(&fc)
	applyDefaults(&fc)

	level, err := parseLogLevel(fc.Logging.Level)
	if err != nil {
		return Config{}, err
	}

	corsOrigins, err := toStringList(fc.CORS.AllowedOrigins)
	if err != nil {
		return Config{}, err
	}
	returnToOrigins, err := toStringList(fc.CORS.ReturnToAllowedOrigins)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		DatabaseURL:               fc.Database.URL,
		TelemetryDatabaseURL:      fc.Database.TelemetryURL,
		ServerAddr:                fc.Server.Addr,
		JWTSigningKey:             fc.Auth.JWTSigningKey,
		GoogleClientID:            fc.Auth.Google.ClientID,
		GoogleClientSecret:        fc.Auth.Google.ClientSecret,
		GoogleRedirectURL:         fc.Auth.Google.LoginRedirectURL,
		GoogleCalendarRedirectURL: fc.Auth.Google.CalendarRedirectURL,
		DevAuth:                   fc.Server.DevAuth,
		CORSAllowedOrigins:        corsOrigins,
		ReturnToAllowedOrigins:    returnToOrigins,
		DailyUsageCapUSD:          fc.Usage.DailyCapUSD,
		UsagePriceTableJSON:       fc.Usage.PriceTable,
		AdminEmails:               splitCSV(fc.Auth.AdminEmails),
		AvatarBucketName:          fc.Storage.AvatarBucketName,
		TCXBucketName:             fc.Storage.TCXBucketName,
		AWSRegion:                 fc.Storage.AWSRegion,
		FatSecretClientID:         fc.NutritionLookup.FatSecretClientID,
		FatSecretClientSecret:     fc.NutritionLookup.FatSecretClientSecret,
		USDAFDCAPIKey:             fc.NutritionLookup.USDAFDCAPIKey,
		CalendarTokenEncKey:       fc.Auth.Google.CalendarTokenEncKey,
		LogLevel:                  level,
		VectorMemory: VectorMemoryConfig{
			Enabled:            fc.VectorMemory.Enabled,
			OpenAIAPIKey:       fc.VectorMemory.OpenAIAPIKey,
			AnthropicAPIKey:    fc.VectorMemory.AnthropicAPIKey,
			DistanceThreshold:  fc.VectorMemory.DistanceThreshold,
			DedupThreshold:     fc.VectorMemory.DedupThreshold,
			TopK:               fc.VectorMemory.TopK,
			SessionIdleMinutes: fc.VectorMemory.SessionIdleMinutes,
			DistillModel:       fc.VectorMemory.DistillModel,
			EmbedModel:         fc.VectorMemory.EmbedModel,
			EmbedDim:           fc.VectorMemory.EmbedDim,
		},
		HRZones: HRZonesConfig{
			PopulationDefaultMaxHR: fc.HRZones.PopulationDefaultMaxHR,
			CalibratedRunThreshold: fc.HRZones.CalibratedRunThreshold,
			RecencyWindowDays:      fc.HRZones.RecencyWindowDays,
			MinReferenceBpm:        fc.HRZones.MinReferenceBpm,
			MaxReferenceBpm:        fc.HRZones.MaxReferenceBpm,
			ZoneUpperBounds:        fc.HRZones.ZoneUpperBounds,
			ZoneNames:              fc.HRZones.ZoneNames,
		},
	}

	if cfg.JWTSigningKey == "" {
		return Config{}, errors.New("config: auth.jwt_signing_key is required (set JWT_SIGNING_KEY)")
	}
	if (cfg.AvatarBucketName != "" || cfg.TCXBucketName != "") && cfg.AWSRegion == "" {
		return Config{}, errors.New("config: storage.aws_region is required when a bucket is configured (set AWS_REGION)")
	}

	return cfg, nil
}

// interpolate resolves every ${VAR} label in the string fields of fc. Only
// string fields can carry a ${VAR} reference — numeric and boolean knobs are
// always literals — so the bool/float/int fields are left untouched. The
// list fields are interpolated later, in toStringList.
func interpolate(fc *fileConfig) {
	fc.Server.Addr = interp(fc.Server.Addr)
	fc.Database.URL = interp(fc.Database.URL)
	fc.Database.TelemetryURL = interp(fc.Database.TelemetryURL)
	fc.Logging.Level = interp(fc.Logging.Level)
	fc.Auth.JWTSigningKey = interp(fc.Auth.JWTSigningKey)
	fc.Auth.AdminEmails = interp(fc.Auth.AdminEmails)
	fc.Auth.Google.ClientID = interp(fc.Auth.Google.ClientID)
	fc.Auth.Google.ClientSecret = interp(fc.Auth.Google.ClientSecret)
	fc.Auth.Google.LoginRedirectURL = interp(fc.Auth.Google.LoginRedirectURL)
	fc.Auth.Google.CalendarRedirectURL = interp(fc.Auth.Google.CalendarRedirectURL)
	fc.Auth.Google.CalendarTokenEncKey = interp(fc.Auth.Google.CalendarTokenEncKey)
	fc.Storage.AvatarBucketName = interp(fc.Storage.AvatarBucketName)
	fc.Storage.TCXBucketName = interp(fc.Storage.TCXBucketName)
	fc.Storage.AWSRegion = interp(fc.Storage.AWSRegion)
	fc.Usage.PriceTable = interp(fc.Usage.PriceTable)
	fc.NutritionLookup.FatSecretClientID = interp(fc.NutritionLookup.FatSecretClientID)
	fc.NutritionLookup.FatSecretClientSecret = interp(fc.NutritionLookup.FatSecretClientSecret)
	fc.NutritionLookup.USDAFDCAPIKey = interp(fc.NutritionLookup.USDAFDCAPIKey)
	fc.VectorMemory.OpenAIAPIKey = interp(fc.VectorMemory.OpenAIAPIKey)
	fc.VectorMemory.AnthropicAPIKey = interp(fc.VectorMemory.AnthropicAPIKey)
	fc.VectorMemory.DistillModel = interp(fc.VectorMemory.DistillModel)
	fc.VectorMemory.EmbedModel = interp(fc.VectorMemory.EmbedModel)
}

// applyEnvOverrides lets a conventional env var override the (already
// interpolated) file value for the known non-secret knobs — generalizing
// the old ${DAILY_USAGE_CAP_USD:-0.67} escape hatch to every knob and giving
// local dev a clean override path. Env var names are preserved exactly as
// they were before centralization, so nothing in infra/secrets has to change
// (SOW Open Q2).
//
// An override applies only when the env var is set to a NON-EMPTY value — the
// same "empty means unset" rule the ${VAR} interpolation layer uses. This
// avoids the footgun where a stray empty `CORS_ALLOWED_ORIGIN=` in a .env or
// `docker run -e` would silently blank a committed literal (disabling CORS,
// the return_to allowlist, etc.) with no error. To intentionally clear a
// value, edit the file literal or point CONFIG_FILE at a replacement.
func applyEnvOverrides(fc *fileConfig) {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		fc.Database.URL = v
	}
	if v := os.Getenv("TELEMETRY_DATABASE_URL"); v != "" {
		fc.Database.TelemetryURL = v
	}
	if v := os.Getenv("SERVER_ADDR"); v != "" {
		fc.Server.Addr = v
	}
	if v := os.Getenv("DEV_AUTH"); v != "" {
		fc.Server.DevAuth = v == "true"
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		fc.Logging.Level = v
	}
	if v := os.Getenv("DAILY_USAGE_CAP_USD"); v != "" {
		// A malformed value keeps the file default rather than failing
		// boot — the cap is additive and must not block startup.
		fc.Usage.DailyCapUSD = parseFloatDefault(v, fc.Usage.DailyCapUSD)
	}
	if v := os.Getenv("USAGE_PRICE_TABLE_JSON"); v != "" {
		fc.Usage.PriceTable = v
	}
	if v := os.Getenv("GOOGLE_REDIRECT_URL"); v != "" {
		fc.Auth.Google.LoginRedirectURL = v
	}
	if v := os.Getenv("GOOGLE_CALENDAR_REDIRECT_URL"); v != "" {
		fc.Auth.Google.CalendarRedirectURL = v
	}
	if v := os.Getenv("CORS_ALLOWED_ORIGIN"); v != "" {
		fc.CORS.AllowedOrigins = v
	}
	if v := os.Getenv("RETURN_TO_ALLOWED_ORIGINS"); v != "" {
		fc.CORS.ReturnToAllowedOrigins = v
	}
}

// applyDefaults fills in the derived defaults that aren't expressed as
// literals in the manifest.
func applyDefaults(fc *fileConfig) {
	if fc.Server.Addr == "" {
		fc.Server.Addr = ":8080"
	}
	// Default the telemetry path next to app.db when the app path is set
	// but the telemetry one isn't. Keeps the common case zero-config while
	// still allowing an explicit override.
	if fc.Database.TelemetryURL == "" && fc.Database.URL != "" {
		fc.Database.TelemetryURL = deriveTelemetryPath(fc.Database.URL)
	}
}

// deriveTelemetryPath returns the conventional telemetry.db path sitting
// alongside the given app.db path.
func deriveTelemetryPath(appDB string) string {
	// "/data/app.db" → "/data/telemetry.db"
	// "./app.db"    → "./telemetry.db"
	// "foo/bar.db"  → "foo/telemetry.db"
	if i := strings.LastIndex(appDB, "/"); i >= 0 {
		return appDB[:i+1] + "telemetry.db"
	}
	return "telemetry.db"
}

// parseFloatDefault parses a float, falling back to def on an empty or
// unparseable string.
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

// splitCSV trims and drops empty entries from a comma-separated string.
// Returns nil for empty input so callers can do a single nil-check instead
// of len()==0. It is the normalizer for ${VAR}-sourced string lists
// (ADMIN_EMAILS) and env-override list values.
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

// parseLogLevel maps a level string to a slog.Level. Empty defaults to info;
// an unrecognized value is a startup error (fail fast beats silently logging
// at the wrong verbosity).
func parseLogLevel(raw string) (slog.Level, error) {
	if raw == "" {
		return slog.LevelInfo, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("config: logging.level: unrecognized level %q (use debug, info, warn, or error)", raw)
	}
	return level, nil
}
