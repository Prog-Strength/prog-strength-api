package progstrength

import (
	"testing"

	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/originmatch"
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

// TestMobileDeepLinkIsReturnToAllowed guards the mobile app's OAuth callback.
//
// The native login opens /auth/google/login?return_to=<deep link>, where the
// deep link is expo-linking's Linking.createURL("/auth/callback"). In a real
// (non-Expo-Go) build that renders as "progstrength:///auth/callback" — a
// custom scheme with NO authority component, so url.Parse leaves Host empty and
// the guarded origin is the bare "progstrength://".
//
// That entry lived in the RETURN_TO_ALLOWED_ORIGINS GitHub secret and was lost
// when the whitelist moved into config.toml: only the two web origins were
// ported, so every mobile login 400'd with "return_to origin is not allowed".
// This test pins the literal the guard actually compares against — note that
// the fuller-looking "progstrength://auth/callback" does NOT work, because the
// match is on origin (scheme + host), not on the whole URL.
func TestMobileDeepLinkIsReturnToAllowed(t *testing.T) {
	t.Setenv("JWT_SIGNING_KEY", "test-signing-key")
	cfg, err := config.Load(DefaultConfigTOML)
	if err != nil {
		t.Fatalf("Load(DefaultConfigTOML) error = %v", err)
	}

	const deepLink = "progstrength:///auth/callback"
	if !originmatch.AllowReturnTo(deepLink, cfg.ReturnToAllowedOrigins) {
		t.Errorf("return_to %q rejected by return_to_allowed_origins %q; "+
			"the mobile OAuth callback needs the bare \"progstrength://\" origin "+
			"in the whitelist or native login fails with "+
			"\"return_to origin is not allowed\"", deepLink, cfg.ReturnToAllowedOrigins)
	}
}
