package progstrength

import (
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
)

// TestEmbeddedConfigLoads guards that the shipped config.toml decodes cleanly.
// A malformed manifest would otherwise only surface at process boot.
func TestEmbeddedConfigLoads(t *testing.T) {
	t.Setenv("JWT_SIGNING_KEY", "test-signing-key")
	if _, err := config.Load(DefaultConfigTOML); err != nil {
		t.Fatalf("Load(DefaultConfigTOML) error = %v", err)
	}
}

// TestCORSOriginsAreReturnToAllowed guards the invariant that every browser
// origin granted credentialed CORS access is also a permitted OAuth return_to
// target. Both lists describe the same first-party frontends: a browser origin
// we trust to call the API with credentials is, by definition, an origin the
// login flow must be able to bounce back to after Google consent.
//
// These two lists drifted once (return_to listed a stale "app." subdomain that
// the apex-served web app never used), which broke login with
// "return_to origin is not allowed". This test fails if they drift again.
//
// The relationship is CORS ⊆ return_to, not equality: return_to may legitimately
// carry extra entries with no CORS counterpart, e.g. the mobile app's custom
// "progstrength://" scheme, which is never a browser Origin.
func TestCORSOriginsAreReturnToAllowed(t *testing.T) {
	t.Setenv("JWT_SIGNING_KEY", "test-signing-key")
	cfg, err := config.Load(DefaultConfigTOML)
	if err != nil {
		t.Fatalf("Load(DefaultConfigTOML) error = %v", err)
	}

	returnTo := make(map[string]bool, len(cfg.ReturnToAllowedOrigins))
	for _, o := range cfg.ReturnToAllowedOrigins {
		returnTo[o] = true
	}

	for _, origin := range cfg.CORSAllowedOrigins {
		if !returnTo[origin] {
			t.Errorf("CORS origin %q is not in return_to_allowed_origins; "+
				"a browser origin trusted for credentialed access must also be a "+
				"permitted OAuth return_to target, or login from that origin fails "+
				"with \"return_to origin is not allowed\"", origin)
		}
	}
}
