package config

import (
	"reflect"
	"testing"
)

// loadWith sets the minimum env for Load to succeed (JWT_SIGNING_KEY is the
// only hard requirement) plus whatever the case under test needs, then
// returns the parsed config.
func loadWith(t *testing.T, env map[string]string) Config {
	t.Helper()
	t.Setenv("JWT_SIGNING_KEY", "test-secret")
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestCORSAllowedOrigins(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want []string
	}{
		{
			name: "unset disables CORS",
			env:  "",
			want: nil,
		},
		{
			name: "single origin (backward compatible)",
			env:  "https://progstrength.fitness",
			want: []string{"https://progstrength.fitness"},
		},
		{
			name: "prod origin plus a wildcard Vercel preview pattern",
			// The wildcard entry is preserved verbatim — go-chi expands the
			// single "*" at match time, covering every branch preview.
			env: "https://progstrength.fitness, https://prog-strength-web-*-acme.vercel.app",
			want: []string{
				"https://progstrength.fitness",
				"https://prog-strength-web-*-acme.vercel.app",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadWith(t, map[string]string{"CORS_ALLOWED_ORIGIN": tt.env})
			if !reflect.DeepEqual(cfg.CORSAllowedOrigins, tt.want) {
				t.Errorf("CORSAllowedOrigins = %#v, want %#v", cfg.CORSAllowedOrigins, tt.want)
			}
		})
	}
}
