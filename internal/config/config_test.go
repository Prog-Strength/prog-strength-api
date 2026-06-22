package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// minimalTOML is the smallest manifest that satisfies the required fields
// (jwt_signing_key); tests build on it.
const minimalTOML = `
[auth]
jwt_signing_key = "test-secret"
`

// load decodes the given manifest (with the supplied env applied) and fails
// the test on a Load error. Use loadErr for the error-path cases.
func load(t *testing.T, tomlStr string, env map[string]string) Config {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := Load([]byte(tomlStr))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func loadErr(t *testing.T, tomlStr string, env map[string]string) error {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	_, err := Load([]byte(tomlStr))
	return err
}

// configEnvVars are every environment variable Load consults — interpolated
// ${VAR} labels plus explicit overrides.
var configEnvVars = []string{
	"CONFIG_FILE",
	"DATABASE_URL", "TELEMETRY_DATABASE_URL", "SERVER_ADDR", "DEV_AUTH",
	"LOG_LEVEL", "DAILY_USAGE_CAP_USD", "USAGE_PRICE_TABLE_JSON",
	"GOOGLE_REDIRECT_URL", "GOOGLE_CALENDAR_REDIRECT_URL",
	"CORS_ALLOWED_ORIGIN", "RETURN_TO_ALLOWED_ORIGINS",
	"JWT_SIGNING_KEY", "ADMIN_EMAILS", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET",
	"CALENDAR_TOKEN_ENC_KEY",
	"AVATAR_BUCKET_NAME", "TCX_BUCKET_NAME", "AWS_REGION",
	"FATSECRET_CLIENT_ID", "FATSECRET_CLIENT_SECRET", "USDA_FDC_API_KEY",
	"OPENAI_API_KEY", "ANTHROPIC_API_KEY",
}

// clearConfigEnv unsets every config env var for the duration of the test so
// the manifest's own literals and defaults are what's under test, regardless
// of what the host shell exports. t.Setenv registers restoration of the
// original value; os.Unsetenv then removes it for the test body.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range configEnvVars {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLiteralsDecodeToTypedFields(t *testing.T) {
	toml := `
[server]
addr = ":9090"
dev_auth = true

[database]
url = "/tmp/app.db"

[logging]
level = "warn"

[auth]
jwt_signing_key = "literal-secret"

[usage]
daily_cap_usd = 1.25

[vectormemory]
enabled = true
top_k = 7
embed_dim = 1536
distill_model = "claude-haiku-4-5-20251001"
`
	cfg := load(t, toml, nil)

	if cfg.ServerAddr != ":9090" {
		t.Errorf("ServerAddr = %q, want :9090", cfg.ServerAddr)
	}
	if !cfg.DevAuth {
		t.Error("DevAuth = false, want true")
	}
	if cfg.DatabaseURL != "/tmp/app.db" {
		t.Errorf("DatabaseURL = %q, want /tmp/app.db", cfg.DatabaseURL)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("LogLevel = %v, want warn", cfg.LogLevel)
	}
	if cfg.JWTSigningKey != "literal-secret" {
		t.Errorf("JWTSigningKey = %q, want literal-secret", cfg.JWTSigningKey)
	}
	if cfg.DailyUsageCapUSD != 1.25 {
		t.Errorf("DailyUsageCapUSD = %v, want 1.25", cfg.DailyUsageCapUSD)
	}
	if !cfg.VectorMemory.Enabled || cfg.VectorMemory.TopK != 7 || cfg.VectorMemory.EmbedDim != 1536 {
		t.Errorf("VectorMemory = %+v, want enabled/top_k=7/embed_dim=1536", cfg.VectorMemory)
	}
}

func TestInterpolationFromEnv(t *testing.T) {
	toml := `
[auth]
jwt_signing_key = "${JWT_SIGNING_KEY}"

[auth.google]
client_id = "${GOOGLE_CLIENT_ID}"

[storage]
tcx_bucket_name = "${TCX_BUCKET_NAME}"
aws_region = "${AWS_REGION}"
`
	cfg := load(t, toml, map[string]string{
		"JWT_SIGNING_KEY":  "from-env",
		"GOOGLE_CLIENT_ID": "client-123",
		"TCX_BUCKET_NAME":  "tcx-bucket",
		"AWS_REGION":       "us-east-2",
	})

	if cfg.JWTSigningKey != "from-env" {
		t.Errorf("JWTSigningKey = %q, want from-env", cfg.JWTSigningKey)
	}
	if cfg.GoogleClientID != "client-123" {
		t.Errorf("GoogleClientID = %q, want client-123", cfg.GoogleClientID)
	}
	if cfg.TCXBucketName != "tcx-bucket" {
		t.Errorf("TCXBucketName = %q, want tcx-bucket", cfg.TCXBucketName)
	}
	if cfg.AWSRegion != "us-east-2" {
		t.Errorf("AWSRegion = %q, want us-east-2", cfg.AWSRegion)
	}
}

func TestInterpolationEmptyIsUnset(t *testing.T) {
	// A ${VAR} resolving to an unset env value behaves as "unset": optional
	// features stay dormant. GOOGLE_CLIENT_ID is unset here.
	toml := `
[auth]
jwt_signing_key = "x"

[auth.google]
client_id = "${GOOGLE_CLIENT_ID}"
`
	cfg := load(t, toml, nil)
	if cfg.GoogleClientID != "" {
		t.Errorf("GoogleClientID = %q, want empty (unset)", cfg.GoogleClientID)
	}
}

func TestEnvOverrideBeatsFileLiteral(t *testing.T) {
	toml := `
[server]
dev_auth = false

[logging]
level = "info"

[auth]
jwt_signing_key = "x"

[usage]
daily_cap_usd = 0.67
`
	cfg := load(t, toml, map[string]string{
		"DEV_AUTH":            "true",
		"LOG_LEVEL":           "error",
		"DAILY_USAGE_CAP_USD": "2.50",
	})

	if !cfg.DevAuth {
		t.Error("DevAuth = false, want true (env override)")
	}
	if cfg.LogLevel != slog.LevelError {
		t.Errorf("LogLevel = %v, want error (env override)", cfg.LogLevel)
	}
	if cfg.DailyUsageCapUSD != 2.50 {
		t.Errorf("DailyUsageCapUSD = %v, want 2.50 (env override)", cfg.DailyUsageCapUSD)
	}
}

func TestEnvOverrideMalformedFloatKeepsFileValue(t *testing.T) {
	toml := `
[auth]
jwt_signing_key = "x"

[usage]
daily_cap_usd = 0.67
`
	cfg := load(t, toml, map[string]string{"DAILY_USAGE_CAP_USD": "not-a-number"})
	if cfg.DailyUsageCapUSD != 0.67 {
		t.Errorf("DailyUsageCapUSD = %v, want 0.67 (malformed override ignored)", cfg.DailyUsageCapUSD)
	}
}

func TestRequiredMissingJWTErrors(t *testing.T) {
	toml := `
[logging]
level = "info"
`
	err := loadErr(t, toml, nil)
	if err == nil {
		t.Fatal("Load() error = nil, want required-missing error")
	}
	if !strings.Contains(err.Error(), "jwt_signing_key") {
		t.Errorf("error = %v, want mention of jwt_signing_key", err)
	}
}

func TestJWTFromInterpolatedEnvSatisfiesRequired(t *testing.T) {
	toml := `
[auth]
jwt_signing_key = "${JWT_SIGNING_KEY}"
`
	// Sub-tests so t.Setenv's per-test scoping keeps the two env states from
	// leaking into each other.
	t.Run("set", func(t *testing.T) {
		if err := loadErr(t, toml, map[string]string{"JWT_SIGNING_KEY": "secret"}); err != nil {
			t.Fatalf("Load() error = %v, want nil", err)
		}
	})
	t.Run("unset", func(t *testing.T) {
		clearConfigEnv(t)
		if err := loadErr(t, toml, nil); err == nil {
			t.Fatal("Load() error = nil, want error when JWT_SIGNING_KEY unset")
		}
	})
}

func TestAWSRegionRequiredWhenBucketSet(t *testing.T) {
	withBucketNoRegion := `
[auth]
jwt_signing_key = "x"

[storage]
tcx_bucket_name = "my-bucket"
aws_region = ""
`
	if err := loadErr(t, withBucketNoRegion, nil); err == nil {
		t.Fatal("Load() error = nil, want aws_region required error")
	} else if !strings.Contains(err.Error(), "aws_region") {
		t.Errorf("error = %v, want mention of aws_region", err)
	}

	withBucketAndRegion := `
[auth]
jwt_signing_key = "x"

[storage]
avatar_bucket_name = "avatars"
aws_region = "us-east-2"
`
	if err := loadErr(t, withBucketAndRegion, nil); err != nil {
		t.Errorf("Load() error = %v, want nil when region set", err)
	}

	noBucketNoRegion := `
[auth]
jwt_signing_key = "x"
`
	if err := loadErr(t, noBucketNoRegion, nil); err != nil {
		t.Errorf("Load() error = %v, want nil when no bucket configured", err)
	}
}

func TestDerivedDefaults(t *testing.T) {
	toml := `
[database]
url = "/data/app.db"

[auth]
jwt_signing_key = "x"
`
	cfg := load(t, toml, nil)
	if cfg.ServerAddr != ":8080" {
		t.Errorf("ServerAddr = %q, want :8080 (default)", cfg.ServerAddr)
	}
	if cfg.TelemetryDatabaseURL != "/data/telemetry.db" {
		t.Errorf("TelemetryDatabaseURL = %q, want /data/telemetry.db (derived)", cfg.TelemetryDatabaseURL)
	}
}

func TestLogLevelValidationRejectsGarbage(t *testing.T) {
	toml := `
[logging]
level = "loud"

[auth]
jwt_signing_key = "x"
`
	err := loadErr(t, toml, nil)
	if err == nil {
		t.Fatal("Load() error = nil, want log-level validation error")
	}
	if !strings.Contains(err.Error(), "level") {
		t.Errorf("error = %v, want mention of level", err)
	}
}

func TestListFieldsAcceptTOMLArray(t *testing.T) {
	toml := `
[auth]
jwt_signing_key = "x"

[cors]
allowed_origins = ["https://progstrength.fitness", "https://prog-strength-web-*-acme.vercel.app"]
return_to_allowed_origins = ["https://app.progstrength.fitness"]
`
	cfg := load(t, toml, nil)
	want := []string{"https://progstrength.fitness", "https://prog-strength-web-*-acme.vercel.app"}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, want) {
		t.Errorf("CORSAllowedOrigins = %#v, want %#v", cfg.CORSAllowedOrigins, want)
	}
	if !reflect.DeepEqual(cfg.ReturnToAllowedOrigins, []string{"https://app.progstrength.fitness"}) {
		t.Errorf("ReturnToAllowedOrigins = %#v", cfg.ReturnToAllowedOrigins)
	}
}

func TestListFieldAcceptsInterpolatedCSVString(t *testing.T) {
	// A list written as a single "${VAR}" decodes to a string, then
	// toStringList interpolates + splits the CSV the env supplies.
	toml := `
[auth]
jwt_signing_key = "x"

[cors]
allowed_origins = "${CORS_ALLOWED_ORIGIN}"
`
	cfg := load(t, toml, map[string]string{
		"CORS_ALLOWED_ORIGIN": "https://a.example, https://b.example",
	})
	want := []string{"https://a.example", "https://b.example"}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, want) {
		t.Errorf("CORSAllowedOrigins = %#v, want %#v", cfg.CORSAllowedOrigins, want)
	}
}

func TestAdminEmailsFromCSV(t *testing.T) {
	toml := `
[auth]
jwt_signing_key = "x"
admin_emails = "${ADMIN_EMAILS}"
`
	cfg := load(t, toml, map[string]string{
		"ADMIN_EMAILS": "ops@example.com, owner@example.com",
	})
	if !reflect.DeepEqual(cfg.AdminEmails, []string{"ops@example.com", "owner@example.com"}) {
		t.Errorf("AdminEmails = %#v", cfg.AdminEmails)
	}
}

func TestCORSEnvOverrideBeatsArrayLiteral(t *testing.T) {
	toml := `
[auth]
jwt_signing_key = "x"

[cors]
allowed_origins = ["https://progstrength.fitness"]
`
	cfg := load(t, toml, map[string]string{
		"CORS_ALLOWED_ORIGIN": "http://localhost:3000",
	})
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, []string{"http://localhost:3000"}) {
		t.Errorf("CORSAllowedOrigins = %#v, want [http://localhost:3000] (env override)", cfg.CORSAllowedOrigins)
	}
}

func TestEmptyEnvOverrideDoesNotBlankFileLiteral(t *testing.T) {
	// A present-but-empty override must NOT clear a committed literal — empty
	// is treated as "unset", consistent with ${VAR} interpolation. This pins
	// the guard against the footgun of a stray `CORS_ALLOWED_ORIGIN=` blanking
	// the CORS allowlist.
	toml := `
[server]
addr = ":8080"

[auth]
jwt_signing_key = "x"

[cors]
allowed_origins = ["https://progstrength.fitness"]
`
	clearConfigEnv(t)
	t.Setenv("CORS_ALLOWED_ORIGIN", "")
	t.Setenv("SERVER_ADDR", "")

	cfg, err := Load([]byte(toml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, []string{"https://progstrength.fitness"}) {
		t.Errorf("CORSAllowedOrigins = %#v, want the file literal preserved", cfg.CORSAllowedOrigins)
	}
	if cfg.ServerAddr != ":8080" {
		t.Errorf("ServerAddr = %q, want :8080 (empty override ignored)", cfg.ServerAddr)
	}
}

func TestConfigFileOverridesEmbeddedDefault(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(dir, "external.toml")
	contents := `
[server]
addr = ":7000"

[auth]
jwt_signing_key = "external-secret"
`
	if err := os.WriteFile(external, []byte(contents), 0o600); err != nil {
		t.Fatalf("write external config: %v", err)
	}
	t.Setenv("CONFIG_FILE", external)

	// The embedded default passed here must be ignored in favor of the file.
	cfg, err := Load([]byte(minimalTOML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ServerAddr != ":7000" {
		t.Errorf("ServerAddr = %q, want :7000 (from CONFIG_FILE)", cfg.ServerAddr)
	}
	if cfg.JWTSigningKey != "external-secret" {
		t.Errorf("JWTSigningKey = %q, want external-secret (from CONFIG_FILE)", cfg.JWTSigningKey)
	}
}

func TestConfigFileMissingErrors(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if _, err := Load([]byte(minimalTOML)); err == nil {
		t.Fatal("Load() error = nil, want error for missing CONFIG_FILE")
	}
}

// TestGoldenManifest decodes the committed config.toml and asserts the
// default Config it yields, with only JWT_SIGNING_KEY supplied (the one
// required value). This is the regression guard that the shipped manifest
// parses and that every field lands where the codebase expects it.
func TestGoldenManifest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.toml"))
	if err != nil {
		t.Fatalf("read committed config.toml: %v", err)
	}
	clearConfigEnv(t)
	t.Setenv("JWT_SIGNING_KEY", "golden-secret")

	cfg, err := Load(data)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := Config{
		DatabaseURL:               "/data/app.db",
		TelemetryDatabaseURL:      "/data/telemetry.db",
		ServerAddr:                ":8080",
		JWTSigningKey:             "golden-secret",
		GoogleClientID:            "",
		GoogleClientSecret:        "",
		GoogleRedirectURL:         "https://api.progstrength.fitness/auth/google/callback",
		GoogleCalendarRedirectURL: "https://api.progstrength.fitness/auth/google/calendar/callback",
		DevAuth:                   false,
		CORSAllowedOrigins: []string{
			"https://progstrength.fitness",
			"https://prog-strength-web-*-jimmy-wallaces-projects.vercel.app",
		},
		ReturnToAllowedOrigins: []string{
			"https://progstrength.fitness",
			"https://prog-strength-web-*-jimmy-wallaces-projects.vercel.app",
		},
		DailyUsageCapUSD:      0.67,
		UsagePriceTableJSON:   "",
		AdminEmails:           nil,
		AvatarBucketName:      "",
		TCXBucketName:         "",
		AWSRegion:             "",
		FatSecretClientID:     "",
		FatSecretClientSecret: "",
		USDAFDCAPIKey:         "",
		CalendarTokenEncKey:   "",
		LogLevel:              slog.LevelInfo,
		VectorMemory: VectorMemoryConfig{
			Enabled:              true,
			OpenAIAPIKey:         "",
			AnthropicAPIKey:      "",
			DistanceThreshold:    0.70,
			DedupThreshold:       0.40,
			TopK:                 5,
			SessionIdleMinutes:   30,
			WorkoutSettleMinutes: 10,
			DistillModel:         "claude-haiku-4-5-20251001",
			EmbedModel:           "text-embedding-3-small",
			EmbedDim:             1536,
		},
		HRZones: HRZonesConfig{
			PopulationDefaultMaxHR: 190,
			CalibratedRunThreshold: 5,
			RecencyWindowDays:      90,
			MinReferenceBpm:        100,
			MaxReferenceBpm:        230,
			ZoneUpperBounds:        []float64{0.60, 0.70, 0.80, 0.90},
			ZoneNames:              []string{"Recovery", "Aerobic", "Tempo", "Threshold", "VO2max"},
		},
	}

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("golden config mismatch:\n got = %#v\nwant = %#v", cfg, want)
	}
}

// TestHRZonesSectionParses pins the [hr_zones] tunables: the committed manifest
// decodes into the typed HRZonesConfig the engine consumes. These are plain
// literals (no ${VAR} interpolation, no env override), so a direct Load of the
// golden config is the assertion surface.
func TestHRZonesSectionParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.toml"))
	if err != nil {
		t.Fatalf("read committed config.toml: %v", err)
	}
	clearConfigEnv(t)
	t.Setenv("JWT_SIGNING_KEY", "hrzones-secret")

	cfg, err := Load(data)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HRZones.PopulationDefaultMaxHR != 190 {
		t.Errorf("PopulationDefaultMaxHR = %d, want 190", cfg.HRZones.PopulationDefaultMaxHR)
	}
	if cfg.HRZones.CalibratedRunThreshold != 5 {
		t.Errorf("CalibratedRunThreshold = %d, want 5", cfg.HRZones.CalibratedRunThreshold)
	}
	if len(cfg.HRZones.ZoneUpperBounds) != 4 {
		t.Errorf("len(ZoneUpperBounds) = %d, want 4", len(cfg.HRZones.ZoneUpperBounds))
	}
	if len(cfg.HRZones.ZoneNames) != 5 || cfg.HRZones.ZoneNames[4] != "VO2max" {
		t.Errorf("ZoneNames = %#v, want [...]/[4]==VO2max", cfg.HRZones.ZoneNames)
	}
}
